package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// containerImage is the in-memory, transport-free OCI image produced by the
// distribution builder. Archive is an OCI image layout tar, not a Docker save
// archive and not a build context.
type containerImage struct {
	Archive        []byte
	ManifestDigest string
}

type containerImageExpectation struct {
	ManifestDigest   string
	BinarySHA        string
	ContainerfileSHA string
	SourceCommit     string
	Version          string
	Platform         string
	Architecture     string
}

const (
	containerOCILayoutVersion  = "1.0.0"
	containerManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	containerConfigMediaType   = "application/vnd.oci.image.config.v1+json"
	containerLayerMediaType    = "application/vnd.oci.image.layer.v1.tar"
	containerBlobPrefix        = "blobs/sha256/"
	containerSchema            = "agent-eval/container-image"
	containerSchemaVersion     = "1"
	containerMaxArchiveBytes   = 128 << 20
	containerMaxEntryBytes     = 64 << 20
	containerMaxEntries        = 8
)

type containerDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type containerPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

type containerIndex struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Manifests     []containerIndexEntry `json:"manifests"`
}

type containerIndexEntry struct {
	MediaType string            `json:"mediaType"`
	Digest    string            `json:"digest"`
	Size      int64             `json:"size"`
	Platform  containerPlatform `json:"platform"`
}

type containerManifest struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Config        containerDescriptor   `json:"config"`
	Layers        []containerDescriptor `json:"layers"`
	Annotations   map[string]string     `json:"annotations"`
}

type containerConfig struct {
	Architecture string                 `json:"architecture"`
	OS           string                 `json:"os"`
	Config       containerRuntimeConfig `json:"config"`
	RootFS       containerRootFS        `json:"rootfs"`
}

type containerRuntimeConfig struct {
	Entrypoint []string          `json:"Entrypoint"`
	Labels     map[string]string `json:"Labels"`
}

type containerRootFS struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"`
}

type containerOCILayout struct {
	ImageLayoutVersion string `json:"imageLayoutVersion"`
}

func buildContainerImage(binary []byte, sourceCommit, version, platform, architecture, binarySHA, containerfileSHA string) (containerImage, error) {
	if len(binary) == 0 || len(binary) > containerMaxEntryBytes {
		return containerImage{}, errors.New("container binary exceeds size bound")
	}
	if err := validateContainerBinding(containerImageExpectation{
		BinarySHA: binarySHA, ContainerfileSHA: containerfileSHA,
		SourceCommit: sourceCommit, Version: version, Platform: platform, Architecture: architecture,
	}); err != nil {
		return containerImage{}, err
	}
	if sha256Bytes(binary) != binarySHA {
		return containerImage{}, errors.New("container binary digest does not match binary bytes")
	}

	layer, err := buildContainerLayer(binary)
	if err != nil {
		return containerImage{}, fmt.Errorf("container layer: %w", err)
	}
	layerDigest := sha256Bytes(layer)
	labels := containerBindingLabels(sourceCommit, version, platform, architecture, binarySHA, containerfileSHA)
	configData, err := canonicalJSON(containerConfig{
		Architecture: architecture,
		OS:           platform,
		Config: containerRuntimeConfig{
			Entrypoint: []string{"/agent-eval"},
			Labels:     labels,
		},
		RootFS: containerRootFS{Type: "layers", DiffIDs: []string{"sha256:" + layerDigest}},
	})
	if err != nil {
		return containerImage{}, fmt.Errorf("container config: %w", err)
	}
	configDigest := sha256Bytes(configData)
	manifestData, err := canonicalJSON(containerManifest{
		SchemaVersion: 2,
		Config:        containerDescriptor{MediaType: containerConfigMediaType, Digest: "sha256:" + configDigest, Size: int64(len(configData))},
		Layers:        []containerDescriptor{{MediaType: containerLayerMediaType, Digest: "sha256:" + layerDigest, Size: int64(len(layer))}},
		Annotations:   labels,
	})
	if err != nil {
		return containerImage{}, fmt.Errorf("container manifest: %w", err)
	}
	manifestDigest := sha256Bytes(manifestData)
	indexData, err := canonicalJSON(containerIndex{
		SchemaVersion: 2,
		Manifests: []containerIndexEntry{{
			MediaType: containerManifestMediaType, Digest: "sha256:" + manifestDigest, Size: int64(len(manifestData)),
			Platform: containerPlatform{OS: platform, Architecture: architecture},
		}},
	})
	if err != nil {
		return containerImage{}, fmt.Errorf("container index: %w", err)
	}
	layoutData, err := canonicalJSON(containerOCILayout{ImageLayoutVersion: containerOCILayoutVersion})
	if err != nil {
		return containerImage{}, fmt.Errorf("container layout: %w", err)
	}
	archive, err := buildContainerArchive(map[string][]byte{
		"oci-layout":                         layoutData,
		"index.json":                         indexData,
		containerBlobPrefix + configDigest:   configData,
		containerBlobPrefix + layerDigest:    layer,
		containerBlobPrefix + manifestDigest: manifestData,
	})
	if err != nil {
		return containerImage{}, err
	}
	return containerImage{Archive: archive, ManifestDigest: manifestDigest}, nil
}

func validateContainerImageArchive(data []byte, want containerImageExpectation) error {
	if len(data) == 0 || len(data) > containerMaxArchiveBytes {
		return errors.New("container archive exceeds size bound")
	}
	if err := validateContainerBinding(want); err != nil {
		return err
	}
	if !validDigest(want.ManifestDigest) {
		return errors.New("container manifest digest is not canonical")
	}
	entries, err := readContainerArchive(data)
	if err != nil {
		return err
	}
	canonical, err := buildContainerArchive(entries)
	if err != nil || !bytes.Equal(canonical, data) {
		return errors.New("container archive is not canonical")
	}

	layoutData, ok := entries["oci-layout"]
	if !ok {
		return errors.New("container archive is missing oci-layout")
	}
	var layout containerOCILayout
	if err := decodeContainerJSON(layoutData, &layout); err != nil || layout.ImageLayoutVersion != containerOCILayoutVersion {
		return errors.New("container OCI layout is invalid")
	}
	indexData, ok := entries["index.json"]
	if !ok {
		return errors.New("container archive is missing index.json")
	}
	var index containerIndex
	if err := decodeContainerJSON(indexData, &index); err != nil || index.SchemaVersion != 2 || len(index.Manifests) != 1 {
		return errors.New("container index is invalid")
	}
	indexEntry := index.Manifests[0]
	if indexEntry.MediaType != containerManifestMediaType || indexEntry.Digest != "sha256:"+want.ManifestDigest || indexEntry.Platform.OS != want.Platform || indexEntry.Platform.Architecture != want.Architecture {
		return errors.New("container index binding drift")
	}
	manifestData, ok := entries[containerBlobPrefix+want.ManifestDigest]
	if !ok || sha256Bytes(manifestData) != want.ManifestDigest || indexEntry.Size != int64(len(manifestData)) {
		return errors.New("container manifest blob drift")
	}
	var manifest containerManifest
	if err := decodeContainerJSON(manifestData, &manifest); err != nil || manifest.SchemaVersion != 2 || len(manifest.Layers) != 1 {
		return errors.New("container manifest is invalid")
	}
	wantLabels := containerBindingLabels(want.SourceCommit, want.Version, want.Platform, want.Architecture, want.BinarySHA, want.ContainerfileSHA)
	if !sameStringMap(manifest.Annotations, wantLabels) {
		return errors.New("container manifest identity or runtime policy drift")
	}
	if manifest.Config.MediaType != containerConfigMediaType || !validContainerDigest(manifest.Config.Digest) || manifest.Config.Size <= 0 {
		return errors.New("container config descriptor is invalid")
	}
	configDigest := strings.TrimPrefix(manifest.Config.Digest, "sha256:")
	configData, ok := entries[containerBlobPrefix+configDigest]
	if !ok || int64(len(configData)) != manifest.Config.Size || sha256Bytes(configData) != configDigest {
		return errors.New("container config blob drift")
	}
	var config containerConfig
	if err := decodeContainerJSON(configData, &config); err != nil || config.OS != want.Platform || config.Architecture != want.Architecture || len(config.Config.Entrypoint) != 1 || config.Config.Entrypoint[0] != "/agent-eval" || !sameStringMap(config.Config.Labels, wantLabels) || config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != 1 {
		return errors.New("container config identity or runtime policy drift")
	}
	layer := manifest.Layers[0]
	if layer.MediaType != containerLayerMediaType || !validContainerDigest(layer.Digest) || layer.Size <= 0 || config.RootFS.DiffIDs[0] != layer.Digest {
		return errors.New("container layer descriptor is invalid")
	}
	layerDigest := strings.TrimPrefix(layer.Digest, "sha256:")
	layerData, ok := entries[containerBlobPrefix+layerDigest]
	if !ok || int64(len(layerData)) != layer.Size || sha256Bytes(layerData) != layerDigest {
		return errors.New("container layer blob drift")
	}
	if err := validateContainerLayer(layerData, want.BinarySHA); err != nil {
		return err
	}
	expected := map[string]bool{
		"oci-layout": true, "index.json": true,
		containerBlobPrefix + want.ManifestDigest: true,
		containerBlobPrefix + configDigest:        true,
		containerBlobPrefix + layerDigest:         true,
	}
	if len(entries) != len(expected) {
		return errors.New("container archive contains unexpected members")
	}
	for name := range entries {
		if !expected[name] {
			return fmt.Errorf("container archive contains unexpected member %q", name)
		}
	}
	return nil
}

func validateContainerBinding(want containerImageExpectation) error {
	if !validCommit(want.SourceCommit) {
		return errors.New("container source commit is not canonical")
	}
	if !validContainerText(want.Version) || !validContainerText(want.Platform) || !validContainerText(want.Architecture) {
		return errors.New("container identity is not canonical")
	}
	if !validDigest(want.BinarySHA) || !validDigest(want.ContainerfileSHA) {
		return errors.New("container artifact digest is not canonical")
	}
	return nil
}

func validContainerText(value string) bool {
	if value == "" || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		if r < '!' || r > '~' || strings.ContainsRune("/\\\x00", r) {
			return false
		}
	}
	return true
}

func validContainerDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validDigest(strings.TrimPrefix(value, "sha256:"))
}

func containerBindingLabels(sourceCommit, version, platform, architecture, binarySHA, containerfileSHA string) map[string]string {
	return map[string]string{
		"io.atl.agent-eval/architecture":         architecture,
		"io.atl.agent-eval/binary-sha256":        binarySHA,
		"io.atl.agent-eval/containerfile-sha256": containerfileSHA,
		"io.atl.agent-eval/credentials":          "none",
		"io.atl.agent-eval/network":              "none",
		"io.atl.agent-eval/platform":             platform,
		"io.atl.agent-eval/sandbox":              "caller_enforced",
		"io.atl.agent-eval/schema":               containerSchema,
		"io.atl.agent-eval/schema-version":       containerSchemaVersion,
		"io.atl.agent-eval/source-commit":        sourceCommit,
		"io.atl.agent-eval/updates":              "none",
		"io.atl.agent-eval/version":              version,
	}
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range right {
		if left[key] != value {
			return false
		}
	}
	return true
}

func decodeContainerJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("container JSON has trailing data")
	}
	canonical, err := canonicalJSON(value)
	if err != nil || !bytes.Equal(canonical, data) {
		return errors.New("container JSON is not canonical")
	}
	return nil
}

func buildContainerLayer(binary []byte) ([]byte, error) {
	var buffer bytes.Buffer
	tw := tar.NewWriter(&buffer)
	header := &tar.Header{
		Name: "agent-eval", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg,
		Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0).UTC(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return nil, err
	}
	if _, err := tw.Write(binary); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func validateContainerLayer(data []byte, wantBinarySHA string) error {
	if len(data) == 0 || len(data) > containerMaxEntryBytes || len(data)%512 != 0 {
		return errors.New("container layer is not a bounded tar")
	}
	reader := tar.NewReader(bytes.NewReader(data))
	var payload []byte
	count := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("container layer tar is invalid")
		}
		count++
		if count > 1 || !safeContainerTarName(header.Name) || header.Name != "agent-eval" || header.Typeflag != tar.TypeReg || header.Mode != 0o755 || header.Size < 0 || header.Size > containerMaxEntryBytes || header.ModTime.Unix() != 0 || header.Uname != "" || header.Gname != "" || header.Linkname != "" || len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
			return errors.New("container layer contains an unsafe or non-canonical entry")
		}
		payload, err = io.ReadAll(io.LimitReader(reader, containerMaxEntryBytes+1))
		if err != nil || int64(len(payload)) != header.Size {
			return errors.New("container layer entry exceeds bound")
		}
	}
	if count != 1 || sha256Bytes(payload) != wantBinarySHA {
		return errors.New("container layer binary digest drift")
	}
	canonical, err := buildContainerLayer(payload)
	if err != nil || !bytes.Equal(canonical, data) {
		return errors.New("container layer is not canonical")
	}
	return nil
}

func safeContainerTarName(name string) bool {
	return name != "" && name == path.Clean(name) && !strings.HasPrefix(name, "/") && !strings.Contains(name, "\\") && !strings.Contains(name, "\x00") && name != "." && !strings.Contains(name, "../") && !strings.HasSuffix(name, "/..")
}

func buildContainerArchive(entries map[string][]byte) ([]byte, error) {
	if len(entries) != 5 {
		return nil, errors.New("container archive has an unexpected member count")
	}
	names := make([]string, 0, len(entries))
	for name, data := range entries {
		if !safeContainerTarName(name) || len(data) > containerMaxEntryBytes {
			return nil, errors.New("container archive member is unsafe or oversized")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	tw := tar.NewWriter(&buffer)
	for _, name := range names {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(entries[name])), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0).UTC()}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tw.Write(entries[name]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if buffer.Len() > containerMaxArchiveBytes {
		return nil, errors.New("container archive exceeds size bound")
	}
	return buffer.Bytes(), nil
}

func readContainerArchive(data []byte) (map[string][]byte, error) {
	if len(data)%512 != 0 {
		return nil, errors.New("container archive is not block aligned")
	}
	reader := tar.NewReader(bytes.NewReader(data))
	entries := make(map[string][]byte)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("container archive tar is invalid")
		}
		if len(entries) >= containerMaxEntries || !safeContainerTarName(header.Name) || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > containerMaxEntryBytes || header.Mode != 0o644 || header.ModTime.Unix() != 0 || header.Uname != "" || header.Gname != "" || header.Linkname != "" || len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
			return nil, errors.New("container archive contains an unsafe, special, or oversized entry")
		}
		if _, exists := entries[header.Name]; exists {
			return nil, errors.New("container archive contains a duplicate entry")
		}
		value, err := io.ReadAll(io.LimitReader(reader, containerMaxEntryBytes+1))
		if err != nil || int64(len(value)) != header.Size {
			return nil, errors.New("container archive entry exceeds bound")
		}
		entries[header.Name] = value
	}
	if len(entries) != 5 {
		return nil, errors.New("container archive has an unexpected member count")
	}
	return entries, nil
}

package executionbackend

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"
)

type snapshot struct {
	files   map[string][]byte
	paths   []string
	entries []snapshotEntry
	digest  string
	bytes   uint64
}

type snapshotEntry struct {
	name      string
	directory bool
}

func ArchiveSHA256(data []byte, maxBytes uint64, maxEntries uint32) (string, error) {
	snapshot, err := decodeArchive(data, maxBytes, maxEntries)
	if err != nil {
		return "", err
	}
	defer snapshot.clear()
	return snapshot.digest, nil
}

func decodeArchive(data []byte, maxBytes uint64, maxEntries uint32) (*snapshot, error) {
	return decodeArchiveContext(context.Background(), data, maxBytes, maxEntries)
}

func decodeArchiveContext(ctx context.Context, data []byte, maxBytes uint64, maxEntries uint32) (*snapshot, error) {
	if ctx == nil {
		return nil, contractError("archive_context")
	}
	if len(data) == 0 || uint64(len(data)) > maxBytes || maxBytes == 0 || maxBytes > MaxArchiveBytes || maxEntries == 0 || maxEntries > MaxSnapshotEntries {
		return nil, contractError("archive_bounds")
	}
	owned := slices.Clone(data)
	defer clear(owned)
	reader := tar.NewReader(bytes.NewReader(owned))
	result := &snapshot{files: make(map[string][]byte)}
	seen := map[string]bool{}
	seenFiles := map[string]bool{}
	previous := ""
	for {
		if err := contextCause(ctx); err != nil {
			result.clear()
			return nil, fmt.Errorf("%w: %w", ErrInterrupted, err)
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			result.clear()
			return nil, contractError("archive_read")
		}
		if header == nil || header.Format != tar.FormatUSTAR || !validArchiveHeader(header) || !validArchivePath(header.Name) ||
			seen[header.Name] || previous >= header.Name || len(seen) >= int(maxEntries) {
			result.clear()
			return nil, contractError("archive_entry")
		}
		seen[header.Name] = true
		previous = header.Name
		if parentFile(seenFiles, header.Name) {
			result.clear()
			return nil, contractError("archive_parent")
		}
		if header.Typeflag == tar.TypeDir {
			result.entries = append(result.entries, snapshotEntry{name: header.Name, directory: true})
			continue
		}
		seenFiles[header.Name] = true
		if header.Size < 0 || uint64(header.Size) > maxBytes-result.bytes {
			result.clear()
			return nil, contractError("archive_size")
		}
		content, err := readArchiveContent(ctx, reader, header.Size)
		if err != nil {
			clear(content)
			result.clear()
			return nil, err
		}
		result.files[header.Name] = content
		result.paths = append(result.paths, header.Name)
		result.entries = append(result.entries, snapshotEntry{name: header.Name})
		result.bytes += uint64(len(content))
	}
	if len(result.files) == 0 {
		result.clear()
		return nil, contractError("archive_empty")
	}
	canonical, err := encodeCanonicalArchive(result)
	if err != nil || !bytes.Equal(canonical, owned) {
		clear(canonical)
		result.clear()
		return nil, contractError("archive_canonical")
	}
	clear(canonical)
	result.digest = snapshotDigest(result)
	return result, nil
}

func readArchiveContent(ctx context.Context, reader io.Reader, size int64) ([]byte, error) {
	content := make([]byte, int(size))
	for offset := 0; offset < len(content); {
		if err := contextCause(ctx); err != nil {
			return content, fmt.Errorf("%w: %w", ErrInterrupted, err)
		}
		end := min(offset+32<<10, len(content))
		read, err := io.ReadFull(reader, content[offset:end])
		offset += read
		if err != nil {
			return content, contractError("archive_content")
		}
	}
	return content, nil
}

func validArchiveHeader(header *tar.Header) bool {
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
		return false
	}
	if header.Linkname != "" || header.PAXRecords != nil || header.Uid != 0 || header.Gid != 0 ||
		header.Uname != "" || header.Gname != "" || !header.ModTime.Equal(time.Unix(0, 0)) || !header.AccessTime.IsZero() || !header.ChangeTime.IsZero() ||
		header.Devmajor != 0 || header.Devminor != 0 {
		return false
	}
	if header.Typeflag == tar.TypeDir {
		return header.Size == 0 && header.Mode == 0o555
	}
	return header.Mode == 0o444
}

func encodeCanonicalArchive(value *snapshot) ([]byte, error) {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range value.entries {
		kind, mode, size := byte(tar.TypeReg), int64(0o444), int64(len(value.files[entry.name]))
		if entry.directory {
			kind, mode, size = tar.TypeDir, 0o555, 0
		}
		header := &tar.Header{Name: entry.name, Typeflag: kind, Mode: mode, Size: size, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if err := writer.WriteHeader(header); err != nil {
			return nil, err
		}
		if !entry.directory {
			if _, err := writer.Write(value.files[entry.name]); err != nil {
				return nil, err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func validArchivePath(value string) bool {
	if len(value) == 0 || len(value) > MaxRelativePath || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") || len(segment) > MaxIdentifierBytes {
			return false
		}
		for _, character := range segment {
			if character < 0x21 || character > 0x7e || character == ':' {
				return false
			}
		}
	}
	return true
}

func parentFile(seen map[string]bool, value string) bool {
	for parent := path.Dir(value); parent != "."; parent = path.Dir(parent) {
		if seen[parent] {
			return true
		}
	}
	return false
}

func snapshotDigest(value *snapshot) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/execution-backend/snapshot/v1\x00"))
	for _, name := range value.paths {
		content := value.files[name]
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(name)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(name))
		binary.BigEndian.PutUint64(length[:], uint64(len(content)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(content)
	}
	for _, entry := range value.entries {
		if !entry.directory || hasArchiveDescendant(value.entries, entry.name) {
			continue
		}
		_, _ = hash.Write([]byte("\x00empty-directory\x00"))
		_, _ = hash.Write([]byte(entry.name))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hasArchiveDescendant(entries []snapshotEntry, directory string) bool {
	prefix := directory + "/"
	for _, entry := range entries {
		if strings.HasPrefix(entry.name, prefix) {
			return true
		}
	}
	return false
}

func (value *snapshot) read(name string) ([]byte, error) {
	if value == nil || !validArchivePath(name) {
		return nil, fmt.Errorf("%w: snapshot path", ErrPolicy)
	}
	content, ok := value.files[name]
	if !ok {
		return nil, fmt.Errorf("%w: snapshot entry", ErrExecution)
	}
	return slices.Clone(content), nil
}

func (value *snapshot) clear() {
	if value == nil {
		return
	}
	for name, content := range value.files {
		clear(content)
		delete(value.files, name)
	}
	clear(value.paths)
	value.paths = nil
	clear(value.entries)
	value.entries = nil
	value.digest = ""
	value.bytes = 0
}

func combinedInputSHA256(values ...*snapshot) string {
	digests := make([]string, len(values))
	for index, value := range values {
		digests[index] = value.digest
	}
	return combinedDigestSHA256(digests)
}

func declaredInputSHA256(mounts []Mount) string {
	digests := make([]string, len(mounts))
	for index, mount := range mounts {
		digests[index] = mount.ContentSHA256
	}
	return combinedDigestSHA256(digests)
}

func combinedDigestSHA256(digests []string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("agent-eval/execution-backend/inputs/v1\x00"))
	for _, digest := range digests {
		decoded, _ := hex.DecodeString(digest)
		_, _ = hash.Write(decoded)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func testStaticEvaluatorBinary(message string) []byte {
	const (
		base       = uint64(0x400000)
		codeOffset = 0x100
	)
	code := []byte{
		0xb8, 0x01, 0x00, 0x00, 0x00, // write(1, message, length)
		0xbf, 0x01, 0x00, 0x00, 0x00,
		0x48, 0xbe, 0, 0, 0, 0, 0, 0, 0, 0,
		0xba, 0, 0, 0, 0,
		0x0f, 0x05,
		0xb8, 0x3c, 0x00, 0x00, 0x00, // exit(0)
		0x31, 0xff,
		0x0f, 0x05,
	}
	messageOffset := codeOffset + len(code)
	binary.LittleEndian.PutUint64(code[12:], base+uint64(messageOffset))
	binary.LittleEndian.PutUint32(code[21:], uint32(len(message)))
	data := make([]byte, messageOffset+len(message))
	copy(data[codeOffset:], code)
	copy(data[messageOffset:], message)
	data[0], data[1], data[2], data[3] = 0x7f, 'E', 'L', 'F'
	data[4], data[5], data[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(data[18:], uint16(elf.EM_X86_64))
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint64(data[24:], base+codeOffset)
	binary.LittleEndian.PutUint64(data[32:], 64)
	binary.LittleEndian.PutUint16(data[52:], 64)
	binary.LittleEndian.PutUint16(data[54:], 56)
	binary.LittleEndian.PutUint16(data[56:], 1)
	binary.LittleEndian.PutUint32(data[64:], 1) // PT_LOAD
	binary.LittleEndian.PutUint32(data[68:], 5) // PF_R|PF_X
	binary.LittleEndian.PutUint64(data[72:], 0)
	binary.LittleEndian.PutUint64(data[80:], base)
	binary.LittleEndian.PutUint64(data[88:], base)
	binary.LittleEndian.PutUint64(data[96:], uint64(len(data)))
	binary.LittleEndian.PutUint64(data[104:], uint64(len(data)))
	binary.LittleEndian.PutUint64(data[112:], 0x1000)
	return data
}

func TestContainerImageIsDeterministicAndBoundToInputs(t *testing.T) {
	binary := testStaticEvaluatorBinary("synthetic evaluator binary\n")
	commit := strings.Repeat("a", 40)
	binarySHA := sha256Bytes(binary)
	compatibilitySHA := strings.Repeat("c", 64)
	dependencySHA := strings.Repeat("e", 64)
	sourceTreeSHA := strings.Repeat("d", 64)
	containerfileSHA := strings.Repeat("b", 64)
	first, err := buildContainerImage(binary, commit, "0.1.0-pre-release", "linux", "amd64", binarySHA, compatibilitySHA, dependencySHA, sourceTreeSHA, containerfileSHA)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildContainerImage(binary, commit, "0.1.0-pre-release", "linux", "amd64", binarySHA, compatibilitySHA, dependencySHA, sourceTreeSHA, containerfileSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Archive, second.Archive) || first.ManifestDigest != second.ManifestDigest {
		t.Fatal("container image was not deterministic")
	}
	want := containerImageExpectation{
		ManifestDigest: first.ManifestDigest, BinarySHA: binarySHA, CompatibilitySHA: compatibilitySHA, DependencySHA: dependencySHA, SourceTreeSHA: sourceTreeSHA, ContainerfileSHA: containerfileSHA,
		SourceCommit: commit, Version: "0.1.0-pre-release", Platform: "linux", Architecture: "amd64",
	}
	if err := validateContainerImageArchive(first.Archive, want); err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}
	want.BinarySHA = sha256Bytes([]byte("different binary"))
	if err := validateContainerImageArchive(first.Archive, want); err == nil {
		t.Fatal("binary identity drift was accepted")
	}
	want.BinarySHA = binarySHA
	want.CompatibilitySHA = strings.Repeat("d", 64)
	if err := validateContainerImageArchive(first.Archive, want); err == nil {
		t.Fatal("compatibility identity drift was accepted")
	}
	want.CompatibilitySHA = compatibilitySHA
	want.SourceTreeSHA = strings.Repeat("e", 64)
	if err := validateContainerImageArchive(first.Archive, want); err == nil {
		t.Fatal("source tree identity drift was accepted")
	}
	want.SourceTreeSHA = sourceTreeSHA
	want.DependencySHA = strings.Repeat("f", 64)
	if err := validateContainerImageArchive(first.Archive, want); err == nil {
		t.Fatal("dependency identity drift was accepted")
	}
	want.DependencySHA = dependencySHA
	want.ContainerfileSHA = strings.Repeat("d", 64)
	if err := validateContainerImageArchive(first.Archive, want); err == nil {
		t.Fatal("Containerfile identity drift was accepted")
	}
}

func TestContainerImageRequiresStaticELF(t *testing.T) {
	commit := strings.Repeat("a", 40)
	digest := strings.Repeat("b", 64)
	for _, binary := range [][]byte{[]byte("#!/bin/sh\nprintf 'not scratch-safe\\n'\n"), testStaticEvaluatorBinary("ok\n")} {
		if len(binary) > 0 && binary[0] == '#' {
			if _, err := buildContainerImage(binary, commit, "0.1.0-pre-release", "linux", "amd64", sha256Bytes(binary), digest, digest, digest, digest); err == nil {
				t.Fatal("script bytes were accepted as a scratch image")
			}
			continue
		}
		image, err := buildContainerImage(binary, commit, "0.1.0-pre-release", "linux", "amd64", sha256Bytes(binary), digest, digest, digest, digest)
		if err != nil {
			t.Fatalf("static ELF was rejected: %v", err)
		}
		if image.ManifestDigest == "" {
			t.Fatal("static ELF image omitted a manifest digest")
		}
	}
}

func TestContainerImageRejectsDynamicELF(t *testing.T) {
	data, err := os.ReadFile("/bin/sh")
	if err != nil {
		t.Skip("no dynamic ELF fixture at /bin/sh")
	}
	commit, digest := strings.Repeat("a", 40), strings.Repeat("b", 64)
	if _, err := buildContainerImage(data, commit, "0.1.0-pre-release", "linux", "amd64", sha256Bytes(data), digest, digest, digest, digest); err == nil {
		t.Fatal("dynamic ELF was accepted as a scratch image")
	}
}

func TestContainerImageRejectsArchiveMutation(t *testing.T) {
	binary := testStaticEvaluatorBinary("synthetic evaluator binary\n")
	commit := strings.Repeat("a", 40)
	binarySHA := sha256Bytes(binary)
	compatibilitySHA := strings.Repeat("c", 64)
	dependencySHA := strings.Repeat("e", 64)
	sourceTreeSHA := strings.Repeat("d", 64)
	containerfileSHA := strings.Repeat("b", 64)
	image, err := buildContainerImage(binary, commit, "0.1.0-pre-release", "linux", "amd64", binarySHA, compatibilitySHA, dependencySHA, sourceTreeSHA, containerfileSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := containerImageExpectation{
		ManifestDigest: image.ManifestDigest, BinarySHA: binarySHA, CompatibilitySHA: compatibilitySHA, DependencySHA: dependencySHA, SourceTreeSHA: sourceTreeSHA, ContainerfileSHA: containerfileSHA,
		SourceCommit: commit, Version: "0.1.0-pre-release", Platform: "linux", Architecture: "amd64",
	}
	mutated := append([]byte(nil), image.Archive...)
	mutated[len(mutated)-1025] ^= 1
	if err := validateContainerImageArchive(mutated, want); err == nil {
		t.Fatal("archive mutation was accepted")
	}
	if err := validateContainerImageArchive(append(append([]byte(nil), image.Archive...), 0), want); err == nil {
		t.Fatal("trailing archive bytes were accepted")
	}
}

func TestContainerImageRejectsCanonicalAndSpecialMemberMutations(t *testing.T) {
	binary := testStaticEvaluatorBinary("synthetic evaluator binary\n")
	commit := strings.Repeat("a", 40)
	binarySHA := sha256Bytes(binary)
	compatibilitySHA := strings.Repeat("c", 64)
	dependencySHA := strings.Repeat("e", 64)
	sourceTreeSHA := strings.Repeat("d", 64)
	containerfileSHA := strings.Repeat("b", 64)
	image, err := buildContainerImage(binary, commit, "0.1.0-pre-release", "linux", "amd64", binarySHA, compatibilitySHA, dependencySHA, sourceTreeSHA, containerfileSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := containerImageExpectation{
		ManifestDigest: image.ManifestDigest, BinarySHA: binarySHA, CompatibilitySHA: compatibilitySHA, DependencySHA: dependencySHA, SourceTreeSHA: sourceTreeSHA, ContainerfileSHA: containerfileSHA,
		SourceCommit: commit, Version: "0.1.0-pre-release", Platform: "linux", Architecture: "amd64",
	}
	entries, err := readContainerArchive(image.Archive)
	if err != nil {
		t.Fatal(err)
	}
	entries["oci-layout"] = append(entries["oci-layout"], '\n')
	mutated, err := buildContainerArchive(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateContainerImageArchive(mutated, want); err == nil {
		t.Fatal("non-canonical OCI JSON was accepted")
	}

	unknownEntries := make(map[string][]byte, len(entries)+1)
	for name, data := range entries {
		unknownEntries[name] = data
	}
	unknownEntries["unexpected"] = []byte("unexpected")
	if err := validateContainerImageArchive(rawContainerArchive(t, unknownEntries, nil), want); err == nil {
		t.Fatal("unexpected OCI archive member was accepted")
	}
	specialEntries := make(map[string][]byte, len(entries))
	for name, data := range entries {
		specialEntries[name] = data
	}
	if err := validateContainerImageArchive(rawContainerArchive(t, specialEntries, &tar.Header{Name: "oci-layout", Typeflag: tar.TypeSymlink, Linkname: "elsewhere", Mode: 0o644}), want); err == nil {
		t.Fatal("special OCI archive member was accepted")
	}
}

func rawContainerArchive(t *testing.T, entries map[string][]byte, replacement *tar.Header) []byte {
	t.Helper()
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	tw := tar.NewWriter(&buffer)
	for _, name := range names {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(entries[name])), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0).UTC()}
		if replacement != nil && name == replacement.Name {
			copyHeader := *replacement
			copyHeader.Format = tar.FormatUSTAR
			header = &copyHeader
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write(entries[name]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

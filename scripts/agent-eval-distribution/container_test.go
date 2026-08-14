package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestContainerImageIsDeterministicAndBoundToInputs(t *testing.T) {
	binary := []byte("synthetic evaluator binary\n")
	commit := strings.Repeat("a", 40)
	binarySHA := sha256Bytes(binary)
	containerfileSHA := strings.Repeat("b", 64)
	first, err := buildContainerImage(binary, commit, "0.1.0-pre-release", "linux", "amd64", binarySHA, containerfileSHA)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildContainerImage(binary, commit, "0.1.0-pre-release", "linux", "amd64", binarySHA, containerfileSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Archive, second.Archive) || first.ManifestDigest != second.ManifestDigest {
		t.Fatal("container image was not deterministic")
	}
	want := containerImageExpectation{
		ManifestDigest: first.ManifestDigest, BinarySHA: binarySHA, ContainerfileSHA: containerfileSHA,
		SourceCommit: commit, Version: "0.1.0-pre-release", Platform: "linux", Architecture: "amd64",
	}
	if err := validateContainerImageArchive(first.Archive, want); err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}
	want.BinarySHA = sha256Bytes([]byte("different binary"))
	if err := validateContainerImageArchive(first.Archive, want); err == nil {
		t.Fatal("binary identity drift was accepted")
	}
}

func TestContainerImageRejectsArchiveMutation(t *testing.T) {
	binary := []byte("synthetic evaluator binary\n")
	commit := strings.Repeat("a", 40)
	binarySHA := sha256Bytes(binary)
	image, err := buildContainerImage(binary, commit, "0.1.0-pre-release", "linux", "amd64", binarySHA, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	want := containerImageExpectation{
		ManifestDigest: image.ManifestDigest, BinarySHA: binarySHA, ContainerfileSHA: strings.Repeat("b", 64),
		SourceCommit: commit, Version: "0.1.0-pre-release", Platform: "linux", Architecture: "amd64",
	}
	mutated := append([]byte(nil), image.Archive...)
	mutated[len(mutated)-1025] ^= 1
	if err := validateContainerImageArchive(mutated, want); err == nil {
		t.Fatal("archive mutation was accepted")
	}
}

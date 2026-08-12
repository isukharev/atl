package executionbackend

import (
	"archive/tar"
	"bytes"
	"testing"
	"time"
)

type tarEntry struct {
	name string
	kind byte
	link string
	data []byte
	mode int64
	uid  int
}

func archiveFixture(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := int64(0o444)
		if kind == tar.TypeDir {
			mode = 0o555
		}
		if entry.mode != 0 {
			mode = entry.mode
		}
		header := &tar.Header{Name: entry.name, Typeflag: kind, Linkname: entry.link, Mode: mode,
			Size: int64(len(entry.data)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR, Uid: entry.uid}
		if kind == tar.TypeDir {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.data) > 0 {
			if _, err := writer.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestExecutionBackendArchiveRejectsEscapesLinksAndConflicts(t *testing.T) {
	valid := archiveFixture(t, tarEntry{name: "data", kind: tar.TypeDir}, tarEntry{name: "data/input.txt", data: []byte("value")})
	if _, err := ArchiveSHA256(valid, MaxArchiveBytes, MaxSnapshotEntries); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"absolute":    archiveFixture(t, tarEntry{name: "/escape", data: []byte("x")}),
		"dotdot":      archiveFixture(t, tarEntry{name: "../escape", data: []byte("x")}),
		"backslash":   archiveFixture(t, tarEntry{name: `dir\\escape`, data: []byte("x")}),
		"symlink":     archiveFixture(t, tarEntry{name: "link", kind: tar.TypeSymlink, link: "target"}),
		"hardlink":    archiveFixture(t, tarEntry{name: "link", kind: tar.TypeLink, link: "target"}),
		"device":      archiveFixture(t, tarEntry{name: "device", kind: tar.TypeChar}),
		"permissions": archiveFixture(t, tarEntry{name: "file", mode: 0o644, data: []byte("x")}),
		"owner":       archiveFixture(t, tarEntry{name: "file", uid: 1, data: []byte("x")}),
		"unsorted":    archiveFixture(t, tarEntry{name: "b", data: []byte("x")}, tarEntry{name: "a", data: []byte("y")}),
		"parent-file": archiveFixture(t, tarEntry{name: "a", data: []byte("x")}, tarEntry{name: "a/b", data: []byte("y")}),
		"duplicate":   archiveFixture(t, tarEntry{name: "a", data: []byte("x")}, tarEntry{name: "a", data: []byte("y")}),
		"trailing":    append(archiveFixture(t, tarEntry{name: "a", data: []byte("x")}), []byte("hidden")...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ArchiveSHA256(data, MaxArchiveBytes, MaxSnapshotEntries); err == nil {
				t.Fatal("accepted hostile archive")
			}
		})
	}
}

func TestExecutionBackendArchiveBindsLogicalBytes(t *testing.T) {
	first := archiveFixture(t, tarEntry{name: "input.txt", data: []byte("first")})
	second := archiveFixture(t, tarEntry{name: "input.txt", data: []byte("second")})
	one, err := ArchiveSHA256(first, MaxArchiveBytes, MaxSnapshotEntries)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ArchiveSHA256(second, MaxArchiveBytes, MaxSnapshotEntries)
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("content drift retained digest")
	}
	emptyDirectory := archiveFixture(t, tarEntry{name: "empty", kind: tar.TypeDir}, tarEntry{name: "input.txt", data: []byte("first")})
	three, err := ArchiveSHA256(emptyDirectory, MaxArchiveBytes, MaxSnapshotEntries)
	if err != nil || three == one {
		t.Fatalf("empty-directory digest=%q base=%q err=%v", three, one, err)
	}
}

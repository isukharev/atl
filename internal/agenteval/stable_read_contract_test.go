package agenteval

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReadStableRootFileContract(t *testing.T) {
	initial := []byte("inventoried\x00bytes")
	changed := []byte("replacement\x00bytes")
	if len(changed) != len(initial) {
		t.Fatal("stable-read fixtures must have equal lengths")
	}

	tests := []struct {
		name       string
		limit      int64
		unixOnly   bool
		mutate     func(*testing.T, *syntheticStableReadFixture)
		want       []byte
		wantErr    string
		wantAnyErr bool
	}{
		{
			name:  "exact bytes",
			limit: int64(len(initial)),
			want:  initial,
		},
		{
			name:  "pre-open inode replacement",
			limit: 1 << 20,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture) {
				t.Helper()
				replacement := filepath.Join(fixture.rootPath, "replacement.bin")
				mustWriteStableReadFile(t, replacement, initial)
				if err := os.Chtimes(replacement, fixture.entry.info.ModTime(), fixture.entry.info.ModTime()); err != nil {
					t.Fatal(err)
				}
				replacementInfo := mustStableReadInfo(t, replacement)
				if os.SameFile(fixture.entry.info, replacementInfo) ||
					!sameSyntheticRootInfo(fixture.entry.info, replacementInfo) {
					t.Fatalf("replacement does not isolate inode identity: original=%+v replacement=%+v", fixture.entry.info, replacementInfo)
				}
				if err := os.Remove(fixture.filePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, fixture.filePath); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "entry changed",
		},
		{
			name:  "same-inode hardlink replacement",
			limit: 1 << 20,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture) {
				t.Helper()
				replacement := filepath.Join(fixture.rootPath, "hardlink.bin")
				if err := os.Link(fixture.filePath, replacement); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
				}
				if err := os.Remove(fixture.filePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, fixture.filePath); err != nil {
					t.Fatal(err)
				}
				current := mustStableReadInfo(t, fixture.filePath)
				if !os.SameFile(fixture.entry.info, current) ||
					!sameSyntheticRootInfo(fixture.entry.info, current) {
					t.Fatalf("hardlink replacement did not preserve inventoried identity: original=%+v current=%+v", fixture.entry.info, current)
				}
			},
			want: initial,
		},
		{
			name:     "symlink replacement",
			limit:    1 << 20,
			unixOnly: true,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture) {
				t.Helper()
				target := filepath.Join(fixture.rootPath, "target.bin")
				mustWriteStableReadFile(t, target, initial)
				if err := os.Remove(fixture.filePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(target), fixture.filePath); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "entry changed",
		},
		{
			name:  "directory replacement",
			limit: 1 << 20,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture) {
				t.Helper()
				if err := os.Remove(fixture.filePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(fixture.filePath, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "entry changed",
		},
		{
			name:     "non-regular socket replacement",
			limit:    1 << 20,
			unixOnly: true,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture) {
				t.Helper()
				if err := os.Remove(fixture.filePath); err != nil {
					t.Fatal(err)
				}
				listener, err := net.Listen("unix", fixture.filePath)
				if err != nil {
					t.Skipf("Unix sockets unavailable: %v", err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				if err := os.Chmod(fixture.filePath, 0o600); err != nil {
					t.Fatal(err)
				}
				if info := mustStableReadInfo(t, fixture.filePath); info.Mode()&os.ModeSocket == 0 {
					t.Fatalf("fixture mode=%v, want socket", info.Mode())
				}
			},
			wantAnyErr: true,
		},
		{
			name:     "permission mode drift",
			limit:    1 << 20,
			unixOnly: true,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture) {
				t.Helper()
				if err := os.Chmod(fixture.filePath, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "entry changed",
		},
		{
			name:    "oversize limit",
			limit:   int64(len(initial) - 1),
			wantErr: "file exceeds 16 bytes",
		},
		{
			name:  "size drift",
			limit: 1 << 20,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture) {
				t.Helper()
				mustWriteStableReadFile(t, fixture.filePath, append(bytes.Clone(initial), '!'))
			},
			wantErr: "entry changed",
		},
		{
			name:  "same-size content and timestamp drift",
			limit: 1 << 20,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture) {
				t.Helper()
				mustWriteStableReadFile(t, fixture.filePath, changed)
				drifted := fixture.entry.info.ModTime().Add(time.Hour)
				if err := os.Chtimes(fixture.filePath, drifted, drifted); err != nil {
					t.Fatal(err)
				}
				current := mustStableReadInfo(t, fixture.filePath)
				if !os.SameFile(fixture.entry.info, current) || sameSyntheticRootInfo(fixture.entry.info, current) {
					t.Fatalf("fixture did not preserve inode while drifting metadata: original=%+v current=%+v", fixture.entry.info, current)
				}
			},
			wantErr: "entry changed",
		},
		{
			name:  "metadata-preserving same-inode content replacement",
			limit: 1 << 20,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture) {
				t.Helper()
				mustWriteStableReadFile(t, fixture.filePath, changed)
				if err := os.Chtimes(fixture.filePath, fixture.entry.info.ModTime(), fixture.entry.info.ModTime()); err != nil {
					t.Fatal(err)
				}
				current := mustStableReadInfo(t, fixture.filePath)
				if !os.SameFile(fixture.entry.info, current) || !sameSyntheticRootInfo(fixture.entry.info, current) {
					t.Fatalf("fixture did not preserve inventoried identity and metadata: original=%+v current=%+v", fixture.entry.info, current)
				}
			},
			want: changed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.unixOnly && runtime.GOOS == "windows" {
				t.Skip("POSIX file-type and permission assertions are not applicable")
			}
			fixture := newSyntheticStableReadFixture(t, initial)
			if test.mutate != nil {
				test.mutate(t, fixture)
			}
			data, err := readStableRootFile(fixture.root, fixture.entry.path, fixture.entry.info, test.limit)
			if test.wantErr != "" || test.wantAnyErr {
				if err == nil {
					t.Fatalf("read data=%q, want rejection", data)
				}
				if test.wantErr != "" && err.Error() != test.wantErr {
					t.Fatalf("error=%q, want %q", err, test.wantErr)
				}
				if data != nil {
					t.Fatalf("rejected read returned data=%q", data)
				}
				for _, forbidden := range []string{fixture.rootPath, string(initial), string(changed)} {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("error leaked rejected root or content: %q", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, test.want) {
				t.Fatalf("data=%q, want exact bytes %q", data, test.want)
			}
		})
	}
}

func TestOpenedStableRootFileContract(t *testing.T) {
	initial := []byte("inventoried\x00bytes")
	changed := []byte("replacement\x00bytes")
	tests := []struct {
		name      string
		unixOnly  bool
		mutate    func(*testing.T, *syntheticStableReadFixture, stableRootFile)
		want      []byte
		wantError bool
	}{
		{
			name: "exact opened descriptor bytes",
			want: initial,
		},
		{
			name: "descriptor size drift",
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture, _ stableRootFile) {
				t.Helper()
				mustWriteStableReadFile(t, fixture.filePath, append(bytes.Clone(initial), '!'))
			},
			wantError: true,
		},
		{
			name:     "descriptor mode drift",
			unixOnly: true,
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture, _ stableRootFile) {
				t.Helper()
				if err := os.Chmod(fixture.filePath, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantError: true,
		},
		{
			name: "metadata-preserving descriptor content replacement",
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture, opened stableRootFile) {
				t.Helper()
				mustWriteStableReadFile(t, fixture.filePath, changed)
				if err := os.Chtimes(fixture.filePath, opened.openedInfo.ModTime(), opened.openedInfo.ModTime()); err != nil {
					t.Fatal(err)
				}
				current, err := opened.file.Stat()
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(opened.openedInfo, current) || !sameSyntheticRootInfo(opened.openedInfo, current) {
					t.Fatalf("descriptor mutation did not restore compared metadata: opened=%+v current=%+v", opened.openedInfo, current)
				}
			},
			want: changed,
		},
		{
			name: "ambient path replacement leaves opened descriptor unchanged",
			mutate: func(t *testing.T, fixture *syntheticStableReadFixture, _ stableRootFile) {
				t.Helper()
				replacement := filepath.Join(fixture.rootPath, "ambient-replacement.bin")
				mustWriteStableReadFile(t, replacement, changed)
				if err := os.Remove(fixture.filePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, fixture.filePath); err != nil {
					t.Fatal(err)
				}
			},
			want: initial,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.unixOnly && runtime.GOOS == "windows" {
				t.Skip("POSIX permission assertions are not applicable")
			}
			fixture := newSyntheticStableReadFixture(t, initial)
			opened, err := openStableRootFile(fixture.root, fixture.entry.path, fixture.entry.info)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = opened.file.Close() })
			if test.mutate != nil {
				test.mutate(t, fixture, opened)
			}
			data, err := opened.readWithinLimit(1 << 20)
			if test.wantError {
				if err == nil {
					t.Fatalf("read data=%q, want rejection", data)
				}
				if err.Error() != "entry changed" {
					t.Fatalf("error=%q, want generic entry-changed rejection", err)
				}
				if data != nil {
					t.Fatalf("rejected read returned data=%q", data)
				}
				for _, forbidden := range []string{fixture.rootPath, string(initial), string(changed)} {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("error leaked rejected root or content: %q", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, test.want) {
				t.Fatalf("data=%q, want exact opened-descriptor bytes %q", data, test.want)
			}
		})
	}
}

func TestStableRootFileOperationAndErrorPrecedence(t *testing.T) {
	data := []byte("exact")
	fixture := newSyntheticStableReadFixture(t, data)
	expected := fixture.entry.info
	otherPath := filepath.Join(fixture.rootPath, "other.bin")
	mustWriteStableReadFile(t, otherPath, data)
	other := mustStableReadInfo(t, otherPath)
	if os.SameFile(expected, other) {
		t.Fatal("scripted final-stat fixture unexpectedly has the inventoried inode")
	}

	t.Run("initial stat rejection closes once and ignores close error", func(t *testing.T) {
		statErr := errors.New("synthetic initial stat failure")
		closeErr := errors.New("synthetic close failure")
		file := &scriptedStableReadFile{
			stats:    []scriptedStableReadStat{{err: statErr}},
			closeErr: closeErr,
		}
		_, err := bindStableRootFile(file, expected)
		if err == nil || err.Error() != "entry changed" || errors.Is(err, statErr) || errors.Is(err, closeErr) {
			t.Fatalf("bind error=%v, want closed generic rejection", err)
		}
		assertScriptedStableReadOperations(t, file, "stat", "close")
		if file.closes != 1 {
			t.Fatalf("close calls=%d, want 1", file.closes)
		}
	})

	readErr := errors.New("synthetic read failure")
	finalStatErr := errors.New("synthetic final stat failure")
	closeErr := errors.New("synthetic close failure")
	tests := []struct {
		name        string
		file        *scriptedStableReadFile
		want        []byte
		wantErr     error
		wantErrText string
	}{
		{
			name: "valid read stat and close",
			file: &scriptedStableReadFile{
				data:  data,
				stats: []scriptedStableReadStat{{info: expected}, {info: expected}},
			},
			want: data,
		},
		{
			name: "read error wins after final stat and close",
			file: &scriptedStableReadFile{
				readErr:  readErr,
				stats:    []scriptedStableReadStat{{info: expected}, {err: finalStatErr}},
				closeErr: closeErr,
			},
			wantErr: readErr,
		},
		{
			name: "final validation wins over close",
			file: &scriptedStableReadFile{
				data:     data,
				stats:    []scriptedStableReadStat{{info: expected}, {info: other}},
				closeErr: closeErr,
			},
			wantErrText: "entry changed",
		},
		{
			name: "close error follows valid read and stat",
			file: &scriptedStableReadFile{
				data:     data,
				stats:    []scriptedStableReadStat{{info: expected}, {info: expected}},
				closeErr: closeErr,
			},
			wantErr: closeErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opened, err := bindStableRootFile(test.file, expected)
			if err != nil {
				t.Fatal(err)
			}
			got, err := opened.readWithinLimit(1 << 20)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error=%v, want %v", err, test.wantErr)
				}
			} else if test.wantErrText != "" {
				if err == nil || err.Error() != test.wantErrText || errors.Is(err, closeErr) {
					t.Fatalf("error=%v, want generic %q before close error", err, test.wantErrText)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, test.want) {
				t.Fatalf("data=%q, want %q", got, test.want)
			}
			assertScriptedStableReadOperations(t, test.file, "stat", "read", "stat", "close")
			if test.file.closes != 1 {
				t.Fatalf("close calls=%d, want 1", test.file.closes)
			}
		})
	}
}

// syntheticStableReadFixture captures only the pre-open observation consumed by
// readStableRootFile. Ambient path reconciliation after a file is opened is
// deliberately outside this primitive and belongs to the aggregate inventory.
type syntheticStableReadFixture struct {
	rootPath string
	filePath string
	root     *os.Root
	entry    syntheticRootEntry
}

func newSyntheticStableReadFixture(t *testing.T, data []byte) *syntheticStableReadFixture {
	t.Helper()
	temp := t.TempDir()
	if err := os.Chmod(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(temp, "stable-root")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(rootPath, "contract.bin")
	mustWriteStableReadFile(t, filePath, data)
	info := mustStableReadInfo(t, filePath)
	if runtime.GOOS != "windows" {
		rootInfo := mustStableReadInfo(t, rootPath)
		if !ownerOnlyMode(rootInfo.Mode()) || !ownerOnlyMode(info.Mode()) {
			t.Fatalf("fixture permissions are not owner-only: root=%v file=%v", rootInfo.Mode(), info.Mode())
		}
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return &syntheticStableReadFixture{
		rootPath: rootPath,
		filePath: filePath,
		root:     root,
		entry:    syntheticRootEntry{path: filepath.Base(filePath), info: info},
	}
}

func mustWriteStableReadFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustStableReadInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

type scriptedStableReadStat struct {
	info os.FileInfo
	err  error
}

type scriptedStableReadFile struct {
	data       []byte
	readErr    error
	stats      []scriptedStableReadStat
	closeErr   error
	operations []string
	closes     int
}

func (file *scriptedStableReadFile) Read(buffer []byte) (int, error) {
	file.operations = append(file.operations, "read")
	if len(file.data) > 0 {
		n := copy(buffer, file.data)
		file.data = file.data[n:]
		return n, io.EOF
	}
	if file.readErr != nil {
		err := file.readErr
		file.readErr = nil
		return 0, err
	}
	return 0, io.EOF
}

func (file *scriptedStableReadFile) Stat() (os.FileInfo, error) {
	file.operations = append(file.operations, "stat")
	if len(file.stats) == 0 {
		return nil, errors.New("unexpected scripted stat")
	}
	result := file.stats[0]
	file.stats = file.stats[1:]
	return result.info, result.err
}

func (file *scriptedStableReadFile) Close() error {
	file.operations = append(file.operations, "close")
	file.closes++
	return file.closeErr
}

func assertScriptedStableReadOperations(t *testing.T, file *scriptedStableReadFile, want ...string) {
	t.Helper()
	if strings.Join(file.operations, ",") != strings.Join(want, ",") {
		t.Fatalf("operations=%v, want %v", file.operations, want)
	}
}

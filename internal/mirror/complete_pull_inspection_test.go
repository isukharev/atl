package mirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func inspectionFixture(t *testing.T) (*Mirror, CompletePullCheckpoint, CompletePullJournalEntry, []CompletePullArtifact) {
	t.Helper()
	m, checkpoint, entry, artifacts := jiraCompletePullFixture(t)
	body, err := json.Marshal(checkpoint.IDs)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.SelectionSHA256 = Hash(body)
	if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	checkpoint.SchemaVersion = completePullCheckpointSchema
	return m, checkpoint, entry, artifacts
}

func inspectionTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%s %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += " " + Hash(body)
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += " " + target
		}
		result[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func inspectUnchanged(t *testing.T, m *Mirror) (CompletePullInspection, error) {
	t.Helper()
	before := inspectionTree(t, m.Root)
	result, err := m.InspectJiraCompletePulls()
	if after := inspectionTree(t, m.Root); !reflect.DeepEqual(before, after) {
		t.Fatal("inspection changed tree bytes, modes, entries or modification times")
	}
	body, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	text := string(body)
	if err != nil {
		text += err.Error()
	}
	for _, private := range []string{m.Root, completePullTestHash, "PROJ-1", "10001", "Native issue", "options_sha256", "selection_sha256"} {
		if strings.Contains(text, private) {
			t.Fatalf("content-free inspection leaked fixture value: %s", text)
		}
	}
	if result.Selected != result.Completed+result.Remaining {
		t.Fatalf("unreconciled counts: %+v", result)
	}
	return result, err
}

func TestJiraCompletePullInspectionEmptyAndDurableProgress(t *testing.T) {
	empty := New(t.TempDir())
	got, err := inspectUnchanged(t, empty)
	if err != nil || got.Status != "empty" || !got.Complete || !got.Healthy || got.Recovery != "none" {
		t.Fatalf("empty=%+v err=%v", got, err)
	}
	m, checkpoint, _, _ := inspectionFixture(t)
	for _, next := range []int{0, 1} {
		checkpoint.NextIndex = next
		if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
			t.Fatal(err)
		}
		got, err := inspectUnchanged(t, m)
		if err != nil || got.Status != "resumable" || !got.Complete || !got.Healthy || got.Checkpoints != 1 || got.Selected != 1 || got.Completed != next || got.Recovery != "rerun_original_command" {
			t.Fatalf("progress=%+v err=%v", got, err)
		}
	}
}

func TestJiraCompletePullInspectionPreservesTransactions(t *testing.T) {
	for _, kind := range []string{"publication", "missing payload", "pre-intent", "journal", "covered journal", "owned journal temp", "owned progress temp", "owned intent temp"} {
		t.Run(kind, func(t *testing.T) {
			m, checkpoint, entry, artifacts := inspectionFixture(t)
			if err := m.PrepareJiraCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
				t.Fatal(err)
			}
			stage, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
			switch kind {
			case "missing payload":
				if err := os.Remove(filepath.Join(stage, "payload-0000")); err != nil {
					t.Fatal(err)
				}
			case "pre-intent":
				if err := os.Remove(filepath.Join(stage, "intent.json")); err != nil {
					t.Fatal(err)
				}
			case "journal", "covered journal", "owned journal temp", "owned progress temp":
				if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
					t.Fatal(err)
				}
				journal, _, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "covered journal" {
					checkpoint.NextIndex = 1
					if err := m.SaveCompletePullCheckpoint(checkpoint); err != nil {
						t.Fatal(err)
					}
				} else if strings.HasPrefix(kind, "owned") {
					name := completePullJournalTemp(journal.WriteToken)
					if kind == "owned progress temp" {
						name = completePullProgressTemp(journal.WriteToken)
					}
					if err := os.WriteFile(filepath.Join(filepath.Dir(stage), name), []byte("interrupted metadata"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "owned intent temp":
				body, err := os.ReadFile(filepath.Join(stage, "intent.json"))
				if err != nil {
					t.Fatal(err)
				}
				var intent completePullPublicationIntent
				if err := json.Unmarshal(body, &intent); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(stage), completePullJournalTemp(intent.WriteToken)), []byte("partial"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := inspectUnchanged(t, m)
			if err == nil || got.Status != "recovery_pending" || !got.Complete || got.Healthy || got.Recovery != "preserve_for_inspection" || got.Checkpoints != 1 || got.Journals+got.Publications != 1 {
				t.Fatalf("pending=%+v err=%v", got, err)
			}
		})
	}
}

func TestJiraCompletePullInspectionRejectsUnsafeState(t *testing.T) {
	for _, kind := range []string{"malformed", "future", "unknown member", "wrong digest", "unsorted", "orphan", "orphan temp", "symlink", "mode", "oversize", "entry cap", "stage cap", "unexpected payload"} {
		t.Run(kind, func(t *testing.T) {
			m, checkpoint, entry, artifacts := inspectionFixture(t)
			path, _ := m.completePullCheckpointPath(checkpoint.SelectorSHA256)
			dir := filepath.Dir(path)
			wantStatus, wantReason := "invalid", "malformed"
			switch kind {
			case "malformed":
				inspectionWrite(t, path, []byte("{"))
			case "future":
				inspectionWrite(t, path, []byte(`{"schema_version":99,"future":"opaque"}`))
				wantReason = "unsupported_schema"
			case "unknown member":
				inspectionWrite(t, path, []byte(`{"schema_version":1,"unknown":true}`))
			case "wrong digest", "unsorted":
				checkpoint.SelectionSHA256 = strings.Repeat("d", 64)
				if kind == "unsorted" {
					checkpoint.IDs = []string{"10", "2"}
					body, _ := json.Marshal(checkpoint.IDs)
					checkpoint.SelectionSHA256 = Hash(body)
				}
				body, _ := json.Marshal(checkpoint)
				inspectionWrite(t, path, body)
			case "orphan":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				wantReason = "orphaned"
			case "orphan temp":
				inspectionWrite(t, filepath.Join(dir, completePullJournalTemp(completePullTestWriteToken)), []byte("partial"))
				wantReason = "orphaned"
			case "symlink":
				outside := filepath.Join(t.TempDir(), "checkpoint")
				if err := os.Rename(path, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
				wantReason = "unreadable"
			case "mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.Truncate(path, CompletePullInspectionMaxBytes+1); err != nil {
					t.Fatal(err)
				}
				wantStatus, wantReason = "inventory_limit", "byte_limit"
			case "entry cap":
				for n := 0; n < CompletePullInspectionMaxEntries; n++ {
					inspectionWrite(t, filepath.Join(dir, fmt.Sprintf("%064x.json", n)), []byte("{}"))
				}
				wantStatus, wantReason = "inventory_limit", "entry_limit"
			case "stage cap", "unexpected payload":
				if err := m.PrepareJiraCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
					t.Fatal(err)
				}
				stage, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
				if kind == "stage cap" {
					for n := 0; n <= maxCompletePullPublicationArtifacts; n++ {
						inspectionWrite(t, filepath.Join(stage, fmt.Sprintf("payload-%04d", n)), nil)
					}
					wantStatus, wantReason = "inventory_limit", "entry_limit"
				} else {
					inspectionWrite(t, filepath.Join(stage, "payload-0999"), nil)
					wantReason = "orphaned"
				}
			}
			got, err := inspectUnchanged(t, m)
			if err == nil || got.Status != wantStatus || got.Reason != wantReason || got.Healthy || got.Complete || got.Recovery != "preserve_for_inspection" {
				t.Fatalf("inspection=%+v err=%v want=%s/%s", got, err, wantStatus, wantReason)
			}
		})
	}
}

func inspectionWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestJiraCompletePullInspectionAggregateByteLimit(t *testing.T) {
	m, checkpoint, _, _ := inspectionFixture(t)
	path, _ := m.completePullCheckpointPath(checkpoint.SelectorSHA256)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	i := completePullInspector{m: m, remaining: int64(len(body)), ownedTemps: make(map[string]int)}
	if err := i.inspect(); err == nil || i.result.Reason != "byte_limit" {
		t.Fatalf("aggregate budget=%+v err=%v", i.result, err)
	}
}

func TestJiraCompletePullInspectionSkipsQualifiedSiblingTransactions(t *testing.T) {
	m, checkpoint, _, _ := inspectionFixture(t)
	sibling := checkpoint
	sibling.Service = CompletePullServiceConfluence
	sibling.SelectorSHA256 = strings.Repeat("d", 64)
	if err := m.SaveCompletePullCheckpoint(sibling); err != nil {
		t.Fatal(err)
	}
	path, _ := m.completePullJournalPath(sibling.SelectorSHA256)
	inspectionWrite(t, path, []byte("sibling transaction in progress"))
	stage, _ := m.completePullPublicationDir(sibling.SelectorSHA256)
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	inspectionWrite(t, filepath.Join(stage, "intent.json"), []byte("sibling intent in progress"))
	got, err := inspectUnchanged(t, m)
	if err != nil || got.Status != "resumable" || got.Checkpoints != 1 || got.Journals != 0 || got.Publications != 0 {
		t.Fatalf("Jira-only inspection=%+v err=%v", got, err)
	}
	// Confluence uses its own mutation lock. Its transaction can change while
	// Jira inspects the stable sibling manifest, without contributing health
	// claims or causing Jira to recover or rewrite it.
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
				if err := os.WriteFile(path, []byte("concurrent sibling transaction"), 0600); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	for range 20 {
		got, err = m.InspectJiraCompletePulls()
		if err != nil || got.Checkpoints != 1 || got.Status != "resumable" {
			break
		}
	}
	close(stop)
	if writerErr := <-done; writerErr != nil {
		t.Fatal(writerErr)
	}
	if err != nil || got.Checkpoints != 1 || got.Status != "resumable" {
		t.Fatalf("concurrent sibling inspection=%+v err=%v", got, err)
	}
}

func TestJiraCompletePullInspectionProgressUsesResumeSemantics(t *testing.T) {
	for _, kind := range []string{"current", "stale binding", "legacy sibling", "current sibling", "future", "invalid range", "unexpected include"} {
		t.Run(kind, func(t *testing.T) {
			m, checkpoint, _, _ := inspectionFixture(t)
			progress := completePullProgress{SchemaVersion: completePullJiraProgressSchema, Service: CompletePullServiceJira, SelectorSHA256: checkpoint.SelectorSHA256, OptionsSHA256: checkpoint.OptionsSHA256, SelectionSHA256: checkpoint.SelectionSHA256, NextIndex: 1}
			switch kind {
			case "stale binding":
				progress.SelectionSHA256 = strings.Repeat("e", 64)
			case "legacy sibling":
				progress.SchemaVersion, progress.Service = 1, ""
			case "current sibling":
				progress.SchemaVersion, progress.Service = 4, CompletePullServiceConfluence
			case "future":
				progress.SchemaVersion = 99
			case "invalid range":
				progress.NextIndex = 2
			case "unexpected include":
				progress.Includes = &CompletePullIncludeProgress{}
			}
			body, err := json.Marshal(progress)
			if err != nil {
				t.Fatal(err)
			}
			path, _ := m.completePullProgressPath(checkpoint.SelectorSHA256)
			inspectionWrite(t, path, body)
			resume, _, resumeErr := m.CompletePullCheckpoint(checkpoint.SelectorSHA256)
			got, inspectErr := inspectUnchanged(t, m)
			if (resumeErr == nil) != (inspectErr == nil) {
				t.Fatalf("inspection differs from resume: %+v err=%v resume=%v", got, inspectErr, resumeErr)
			}
			if resumeErr == nil && got.Completed != resume.NextIndex {
				t.Fatalf("prefix=%d want=%d", got.Completed, resume.NextIndex)
			}
			if kind == "future" && got.Reason != "unsupported_schema" {
				t.Fatalf("future=%+v", got)
			}
		})
	}
}

func TestJiraCompletePullInspectionQualifiesSiblingOwnedTemps(t *testing.T) {
	for _, owner := range []string{"journal", "intent"} {
		for _, kind := range []string{"journal temp", "progress temp", "forged token", "misbound owner"} {
			t.Run(owner+"/"+kind, func(t *testing.T) {
				m, checkpoint, entry, artifacts := completePullPublicationFixture(t)
				jira := CompletePullCheckpoint{Service: CompletePullServiceJira, SelectorSHA256: strings.Repeat("d", 64), OptionsSHA256: strings.Repeat("b", 64), IDs: []string{"10001"}}
				ids, _ := json.Marshal(jira.IDs)
				jira.SelectionSHA256 = Hash(ids)
				if err := m.SaveCompletePullCheckpoint(jira); err != nil {
					t.Fatal(err)
				}
				if err := m.PrepareCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
					t.Fatal(err)
				}
				stage, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
				ownerPath := filepath.Join(stage, "intent.json")
				if owner == "journal" {
					if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
						t.Fatal(err)
					}
					ownerPath, _ = m.completePullJournalPath(checkpoint.SelectorSHA256)
				}
				body, err := os.ReadFile(ownerPath)
				if err != nil {
					t.Fatal(err)
				}
				var value map[string]any
				if err := json.Unmarshal(body, &value); err != nil {
					t.Fatal(err)
				}
				token := value["write_token"].(string)
				if kind == "forged token" {
					token = strings.Repeat("0", 32)
					if token == value["write_token"] {
						t.Fatal("fixture token collision")
					}
				}
				if kind == "misbound owner" {
					value["selection_sha256"] = strings.Repeat("e", 64)
					body, err = json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					inspectionWrite(t, ownerPath, body)
				}
				name := completePullJournalTemp(token)
				if kind == "progress temp" {
					name = completePullProgressTemp(token)
				}
				inspectionWrite(t, filepath.Join(filepath.Dir(stage), name), []byte("partial metadata"))
				got, err := inspectUnchanged(t, m)
				if kind == "forged token" || kind == "misbound owner" {
					if err == nil || got.Status != "invalid" || got.Healthy || got.Complete || got.Recovery != "preserve_for_inspection" {
						t.Fatalf("unqualified owner=%+v err=%v", got, err)
					}
				} else if err != nil || got.Status != "resumable" || got.Checkpoints != 1 || got.Journals != 0 || got.Publications != 0 || !got.Complete || !got.Healthy {
					t.Fatalf("sibling owner affected Jira counts/health: %+v err=%v", got, err)
				}
			})
		}
	}
}

func TestJiraCompletePullInspectionStageTempMetadataBudget(t *testing.T) {
	for _, owner := range []string{"pre-intent", "intent"} {
		for _, count := range []int{3, 5} {
			t.Run(fmt.Sprintf("%s/%d", owner, count), func(t *testing.T) {
				m, checkpoint, entry, artifacts := inspectionFixture(t)
				if err := m.PrepareJiraCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
					t.Fatal(err)
				}
				stage, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
				if owner == "pre-intent" {
					if err := os.Remove(filepath.Join(stage, "intent.json")); err != nil {
						t.Fatal(err)
					}
				}
				for index := range count {
					path := filepath.Join(stage, fmt.Sprintf(".tmp-%016x", index))
					inspectionWrite(t, path, nil)
					// Sparse metadata candidates exercise the aggregate bound without
					// allocating or reading large staged payload bodies.
					if err := os.Truncate(path, 16<<20); err != nil {
						t.Fatal(err)
					}
				}
				got, err := m.InspectJiraCompletePulls()
				if count == 5 {
					if err == nil || got.Status != "inventory_limit" || got.Reason != "byte_limit" || got.Complete || got.Healthy || got.Recovery != "preserve_for_inspection" {
						t.Fatalf("metadata cap=%+v err=%v", got, err)
					}
				} else if err == nil || got.Status != "recovery_pending" || !got.Complete || got.Publications != 1 {
					t.Fatalf("bounded metadata=%+v err=%v", got, err)
				}
				for index := range count {
					info, err := os.Stat(filepath.Join(stage, fmt.Sprintf(".tmp-%016x", index)))
					if err != nil || info.Size() != 16<<20 || info.Mode().Perm() != 0600 {
						t.Fatalf("inspection changed sparse metadata: info=%v err=%v", info, err)
					}
				}
			})
		}
	}
}

func TestJiraCompletePullInspectionPreIntentPayloadsAreNotMetadata(t *testing.T) {
	m, checkpoint, _, _ := inspectionFixture(t)
	stage, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	for index := range 5 {
		path := filepath.Join(stage, fmt.Sprintf("payload-%04d", index))
		inspectionWrite(t, path, nil)
		if err := os.Truncate(path, 16<<20); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.InspectJiraCompletePulls()
	if err == nil || got.Status != "recovery_pending" || !got.Complete || got.Publications != 1 {
		t.Fatalf("payloads charged as metadata: %+v err=%v", got, err)
	}
}

func TestJiraCompletePullInspectionRejectsTransactionBindings(t *testing.T) {
	for _, kind := range []string{"future journal", "future intent", "journal binding", "journal range", "intent binding", "unsafe residue"} {
		t.Run(kind, func(t *testing.T) {
			m, checkpoint, entry, artifacts := inspectionFixture(t)
			if err := m.PrepareJiraCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
				t.Fatal(err)
			}
			stage, _ := m.completePullPublicationDir(checkpoint.SelectorSHA256)
			path := filepath.Join(stage, "intent.json")
			if strings.Contains(kind, "journal") {
				if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
					t.Fatal(err)
				}
				path, _ = m.completePullJournalPath(checkpoint.SelectorSHA256)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(body, &value); err != nil {
				t.Fatal(err)
			}
			wantReason := "malformed"
			switch kind {
			case "future journal", "future intent":
				value["schema_version"], value["future"] = 99, true
				wantReason = "unsupported_schema"
			case "journal binding", "intent binding":
				value["options_sha256"] = strings.Repeat("f", 64)
			case "journal range":
				value["start_index"] = 1
			case "unsafe residue":
				if err := os.Chmod(filepath.Join(stage, "payload-0000"), 0644); err != nil {
					t.Fatal(err)
				}
				wantReason = "orphaned"
			}
			body, err = json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			inspectionWrite(t, path, body)
			got, err := inspectUnchanged(t, m)
			if err == nil || got.Status != "invalid" || got.Reason != wantReason {
				t.Fatalf("inspection=%+v err=%v", got, err)
			}
		})
	}
}

func FuzzJiraCompletePullInspectionMetadata(f *testing.F) {
	for _, seed := range []string{`{}`, `{"schema_version":99}`, `{"schema_version":1,"ids":["../escape"]}`, `null`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 8192 {
			t.Skip()
		}
		m, checkpoint, _, _ := inspectionFixture(t)
		path, _ := m.completePullCheckpointPath(checkpoint.SelectorSHA256)
		inspectionWrite(t, path, []byte(body))
		got, err := inspectUnchanged(t, m)
		if err != nil && !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("unexpected error class: %v", err)
		}
		if err != nil && got.Healthy {
			t.Fatal("invalid metadata reported healthy")
		}
	})
}

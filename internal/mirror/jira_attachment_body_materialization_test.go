package mirror

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/domain"
)

func jiraAttachmentBodyMaterializationFixture(t *testing.T, count int) (*Mirror, SyncState) {
	t.Helper()
	m, checkpoint, entry, artifacts := jiraCompletePullCurrentEvidenceFixture(t)
	metadata := artifacts[1].Data
	origin := "sha256:" + strings.Repeat("a", 64)
	attachments := make([]AttachmentSidecarRecord, count)
	for index := range attachments {
		attachments[index] = AttachmentSidecarRecord{
			ID: string(rune('7' + index)), Filename: "fixture-" + string(rune('a'+index)) + ".bin",
			MediaType: "application/octet-stream", DeclaredSize: 3,
			Body: AttachmentSidecarBody{State: AttachmentBodyNotRequested},
		}
	}
	sidecar, err := EncodeAttachmentSidecarV1(AttachmentSidecarV1{
		SchemaVersion: AttachmentSidecarSchemaV1, Service: CorpusSnapshotJira, OriginSHA256: origin,
		ParentID: entry.Identity, ParentRevision: "2026-01-01", NativeSHA256: entry.State.Hash, MetadataSHA256: Hash(metadata),
		InventoryComplete: true, BodiesState: AttachmentBodiesNotRequested, Complete: true,
		Count: len(attachments), PartialReasons: []AttachmentPartialReason{}, Attachments: attachments,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range artifacts {
		if artifacts[index].Path.String() == "PROJ/PROJ-1.attachments.json" {
			artifacts[index].Data = sidecar
		}
	}
	if err := m.PrepareJiraCompletePullPublication(checkpoint, 0, entry, true, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverCompletePullPublication(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RecoverCompletePullJournal(checkpoint.SelectorSHA256, checkpoint, true); err != nil {
		t.Fatal(err)
	}
	return m, entry.State
}

func TestJiraAttachmentBodyMaterializationPublishesOneBodyAtATime(t *testing.T) {
	m, state := jiraAttachmentBodyMaterializationFixture(t, 2)
	inventories, err := m.JiraAttachmentBodyInventories()
	if err != nil || len(inventories) != 1 || inventories[0].BodiesState != AttachmentBodiesNotRequested {
		t.Fatalf("initial inventories=%+v err=%v", inventories, err)
	}
	candidate, attachment, err := m.JiraAttachmentBodyCandidate("10001", "7")
	if err != nil || candidate.Identity != "10001" || candidate.BodiesState != AttachmentBodiesNotRequested || attachment.ID != "7" || attachment.Body.State != AttachmentBodyNotRequested {
		t.Fatalf("first candidate=%+v attachment=%+v err=%v", candidate, attachment, err)
	}
	if err := m.PublishJiraAttachmentBody("10001", "7", []byte("one")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments.json"))
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := DecodeAttachmentSidecarV1(data)
	if err != nil || sidecar.BodiesState != AttachmentBodiesPartial || sidecar.Attachments[0].Body.State != AttachmentBodyCaptured ||
		sidecar.Attachments[1].Body != (AttachmentSidecarBody{State: AttachmentBodyExcluded, Reason: AttachmentBodyReasonAggregateLimit}) {
		t.Fatalf("first sidecar=%+v err=%v", sidecar, err)
	}
	candidate, attachment, err = m.JiraAttachmentBodyCandidate("10001", "8")
	if err != nil || candidate.BodiesState != AttachmentBodiesPartial || attachment.ID != "8" || attachment.Body.Reason != AttachmentBodyReasonAggregateLimit {
		t.Fatalf("partial candidate=%+v attachment=%+v err=%v", candidate, attachment, err)
	}
	if err := m.PublishJiraAttachmentBody("10001", "8", []byte("two")); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments.json"))
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err = DecodeAttachmentSidecarV1(data)
	if err != nil || sidecar.BodiesState != AttachmentBodiesComplete || !sidecar.Complete || len(sidecar.PartialReasons) != 0 {
		t.Fatalf("final sidecar=%+v err=%v", sidecar, err)
	}
	for _, id := range []string{"7", "8"} {
		body, readErr := os.ReadFile(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments", id+".body"))
		if readErr != nil || len(body) != 3 {
			t.Fatalf("body %s=%q err=%v", id, body, readErr)
		}
	}
	info, err := os.Stat(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("attachment directory mode=%v err=%v", info.Mode(), err)
	}
	if inventories, verifyErr := m.JiraAttachmentBodyInventories(); verifyErr != nil || len(inventories) != 1 || inventories[0].BodiesState != AttachmentBodiesComplete {
		t.Fatalf("verified inventories=%+v err=%v state=%+v", inventories, verifyErr, state)
	}
	for _, attachmentID := range []string{"7", "9"} {
		if _, _, candidateErr := m.JiraAttachmentBodyCandidate("10001", attachmentID); !errors.Is(candidateErr, domain.ErrCheckFailed) {
			t.Fatalf("completed candidate %q error=%v", attachmentID, candidateErr)
		}
	}
}

func TestJiraAttachmentBodyMaterializationCandidateRequiresOnePendingRecord(t *testing.T) {
	m, _ := jiraAttachmentBodyMaterializationFixture(t, 1)
	for _, target := range []struct {
		identity     string
		attachmentID string
	}{
		{identity: "0", attachmentID: "7"},
		{identity: "10002", attachmentID: "7"},
		{identity: "10001", attachmentID: "99"},
	} {
		if _, _, err := m.JiraAttachmentBodyCandidate(target.identity, target.attachmentID); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("candidate identity=%q attachment=%q error=%v", target.identity, target.attachmentID, err)
		}
	}
	if err := m.PublishJiraAttachmentBody("10001", "7", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.JiraAttachmentBodyCandidate("10001", "7"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("captured attachment candidate error=%v", err)
	}
}

func TestJiraAttachmentBodyMaterializationRejectsInvalidSuccessorPayload(t *testing.T) {
	m, state := jiraAttachmentBodyMaterializationFixture(t, 1)
	inspection, found, err := m.inspectJiraAttachmentBodyInventoryForState(state)
	if err != nil || !found {
		t.Fatalf("inspection found=%t err=%v", found, err)
	}
	for _, target := range []struct {
		attachmentID string
		body         []byte
	}{
		{attachmentID: "99", body: []byte("one")},
		{attachmentID: "7", body: []byte("wrong-size")},
	} {
		if _, _, err := nextJiraAttachmentBodySidecar(inspection, target.attachmentID, target.body); !errors.Is(err, domain.ErrCheckFailed) {
			t.Fatalf("successor attachment=%q body=%q error=%v", target.attachmentID, target.body, err)
		}
	}
}

func TestJiraAttachmentMaterializationOrderingIsNumeric(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		want        bool
	}{
		{left: "2", right: "10", want: true},
		{left: "10", right: "2", want: false},
		{left: "10", right: "10", want: false},
	} {
		if got := jiraAttachmentMaterializationLess(tc.left, tc.right); got != tc.want {
			t.Fatalf("less(%q, %q)=%t want %t", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestJiraAttachmentBodyMaterializationProgressAcceptsOnlyResumableStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sidecar AttachmentSidecarV1
		pending int
		valid   bool
	}{
		{
			name: "empty not requested inventory",
			sidecar: AttachmentSidecarV1{
				InventoryComplete: true, BodiesState: AttachmentBodiesNotRequested,
			},
			valid: true,
		},
		{
			name: "partial aggregate-limit row",
			sidecar: AttachmentSidecarV1{
				InventoryComplete: true, BodiesState: AttachmentBodiesPartial,
				Attachments: []AttachmentSidecarRecord{{Body: AttachmentSidecarBody{State: AttachmentBodyExcluded, Reason: AttachmentBodyReasonAggregateLimit}}},
			},
			pending: 1, valid: true,
		},
		{
			name: "completed captured row",
			sidecar: AttachmentSidecarV1{
				InventoryComplete: true, BodiesState: AttachmentBodiesComplete,
				Attachments: []AttachmentSidecarRecord{{Body: AttachmentSidecarBody{State: AttachmentBodyCaptured}}},
			},
			valid: true,
		},
		{
			name: "incomplete inventory",
			sidecar: AttachmentSidecarV1{
				BodiesState: AttachmentBodiesNotRequested,
			},
		},
		{
			name: "nonresumable exclusion",
			sidecar: AttachmentSidecarV1{
				InventoryComplete: true, BodiesState: AttachmentBodiesPartial,
				Attachments: []AttachmentSidecarRecord{{Body: AttachmentSidecarBody{State: AttachmentBodyExcluded, Reason: AttachmentBodyReasonMediaExcluded}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pending, err := jiraAttachmentBodyProgress(tc.sidecar)
			if tc.valid {
				if err != nil || pending != tc.pending {
					t.Fatalf("pending=%d err=%v", pending, err)
				}
				return
			}
			if !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestJiraAttachmentBodyMaterializationRecoversPublishedBodyBeforeSidecar(t *testing.T) {
	m, state := jiraAttachmentBodyMaterializationFixture(t, 1)
	inspection, found, err := m.inspectJiraAttachmentBodyInventoryForState(state)
	if err != nil || !found {
		t.Fatalf("inspection found=%t err=%v", found, err)
	}
	next, nextData, err := nextJiraAttachmentBodySidecar(inspection, "7", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	dir := m.jiraAttachmentBodyPublicationDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	token := completePullTestWriteToken
	intent := jiraAttachmentBodyPublicationIntent{
		SchemaVersion: jiraAttachmentBodyPublicationSchema, Identity: "10001", State: state, AttachmentID: "7",
		SidecarSHA256: Hash(inspection.sidecarData), SidecarSize: int64(len(inspection.sidecarData)),
		NextSHA256: Hash(nextData), NextSize: int64(len(nextData)), BodySHA256: Hash([]byte("one")), BodySize: 3,
		Next: 1, WriteToken: token,
	}
	if err := os.WriteFile(filepath.Join(dir, "body.bin"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sidecar.json"), nextData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments", "7.body"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverJiraAttachmentBodyMaterialization(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("recovered stage remains: %v", err)
	}
	if inventories, verifyErr := m.JiraAttachmentBodyInventories(); verifyErr != nil || len(inventories) != 1 || inventories[0].BodiesState != AttachmentBodiesComplete || next.BodiesState != AttachmentBodiesComplete {
		t.Fatalf("inventories=%+v err=%v", inventories, verifyErr)
	}
}

func TestJiraAttachmentBodyMaterializationRecoversCommittedCleanupResidue(t *testing.T) {
	for _, residue := range []struct {
		name  string
		files []string
	}{
		{name: "body already retired", files: []string{"sidecar.json"}},
		{name: "sidecar already retired", files: []string{"body.bin"}},
		{name: "all staged payloads already retired"},
	} {
		t.Run(residue.name, func(t *testing.T) {
			m, state := jiraAttachmentBodyMaterializationFixture(t, 1)
			inspection, found, err := m.inspectJiraAttachmentBodyInventoryForState(state)
			if err != nil || !found {
				t.Fatalf("inspection found=%t err=%v", found, err)
			}
			next, nextData, err := nextJiraAttachmentBodySidecar(inspection, "7", []byte("one"))
			if err != nil {
				t.Fatal(err)
			}
			dir := m.jiraAttachmentBodyPublicationDir()
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			bodyDir := filepath.Join(m.Root, "PROJ", "PROJ-1.attachments")
			if err := os.MkdirAll(bodyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(bodyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bodyDir, "7.body"), []byte("one"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments.json"), nextData, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments.json"), 0o600); err != nil {
				t.Fatal(err)
			}
			intent := jiraAttachmentBodyPublicationIntent{
				SchemaVersion: jiraAttachmentBodyPublicationSchema, Identity: "10001", State: state, AttachmentID: "7",
				SidecarSHA256: Hash(inspection.sidecarData), SidecarSize: int64(len(inspection.sidecarData)),
				NextSHA256: Hash(nextData), NextSize: int64(len(nextData)), BodySHA256: Hash([]byte("one")), BodySize: 3,
				Next: 2, Committed: true, WriteToken: completePullTestWriteToken,
			}
			if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
				t.Fatal(err)
			}
			for _, file := range residue.files {
				data := []byte("one")
				if file == "sidecar.json" {
					data = nextData
				}
				if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.RecoverJiraAttachmentBodyMaterialization(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("cleanup stage remains: %v", err)
			}
			if inventories, verifyErr := m.JiraAttachmentBodyInventories(); verifyErr != nil || len(inventories) != 1 || inventories[0].BodiesState != AttachmentBodiesComplete || next.BodiesState != AttachmentBodiesComplete {
				t.Fatalf("inventories=%+v err=%v", inventories, verifyErr)
			}
		})
	}
}

func TestJiraAttachmentBodyMaterializationRetiresEmptyStageBeforeNextRead(t *testing.T) {
	m, _ := jiraAttachmentBodyMaterializationFixture(t, 1)
	dir := m.jiraAttachmentBodyPublicationDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverJiraAttachmentBodyMaterialization(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("empty stage remains: %v", err)
	}
}

func TestJiraAttachmentBodyMaterializationPreservesUnsafeStage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, m *Mirror, state SyncState)
	}{
		{
			name: "permissive stage mode",
			setup: func(t *testing.T, m *Mirror, _ SyncState) {
				t.Helper()
				dir := m.jiraAttachmentBodyPublicationDir()
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unknown residue without intent",
			setup: func(t *testing.T, m *Mirror, _ SyncState) {
				t.Helper()
				dir := m.jiraAttachmentBodyPublicationDir()
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "unknown"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "stage directory exceeds its entry cap",
			setup: func(t *testing.T, m *Mirror, _ SyncState) {
				t.Helper()
				dir := m.jiraAttachmentBodyPublicationDir()
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"intent.json", "body.bin", "sidecar.json", "extra"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "tampered staged payload",
			setup: func(t *testing.T, m *Mirror, state SyncState) {
				t.Helper()
				inspection, found, err := m.inspectJiraAttachmentBodyInventoryForState(state)
				if err != nil || !found {
					t.Fatalf("inspection found=%t err=%v", found, err)
				}
				_, nextData, err := nextJiraAttachmentBodySidecar(inspection, "7", []byte("one"))
				if err != nil {
					t.Fatal(err)
				}
				dir := m.jiraAttachmentBodyPublicationDir()
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				intent := jiraAttachmentBodyPublicationIntent{
					SchemaVersion: jiraAttachmentBodyPublicationSchema, Identity: "10001", State: state, AttachmentID: "7",
					SidecarSHA256: Hash(inspection.sidecarData), SidecarSize: int64(len(inspection.sidecarData)),
					NextSHA256: Hash(nextData), NextSize: int64(len(nextData)), BodySHA256: Hash([]byte("one")), BodySize: 3,
					Next: -1, WriteToken: completePullTestWriteToken,
				}
				if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "body.bin"), []byte("bad"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, state := jiraAttachmentBodyMaterializationFixture(t, 1)
			tc.setup(t, m, state)
			if err := m.RecoverJiraAttachmentBodyMaterialization(); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("unsafe stage error=%v", err)
			}
		})
	}
}

func TestJiraAttachmentBodyMaterializationPublicControlStateIsFailClosed(t *testing.T) {
	var unavailable *Mirror
	if err := unavailable.RefuseActiveCompletePullState(); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unavailable control state error=%v", err)
	}
	if err := unavailable.RecoverJiraAttachmentBodyMaterialization(); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unavailable recovery error=%v", err)
	}
	if _, err := unavailable.JiraAttachmentBodyInventories(); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unavailable inventories error=%v", err)
	}
	if _, _, err := unavailable.JiraAttachmentBodyCandidate("1", "1"); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unavailable candidate error=%v", err)
	}
	if err := unavailable.PublishJiraAttachmentBody("1", "1", []byte("one")); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("unavailable publication error=%v", err)
	}

	if err := New(t.TempDir()).RefuseActiveCompletePullState(); err != nil {
		t.Fatalf("absent complete-pull state error=%v", err)
	}
	m, _ := jiraAttachmentBodyMaterializationFixture(t, 1)
	if err := m.RemoveCompletePullCheckpoint(completePullTestHash); err != nil {
		t.Fatal(err)
	}
	if err := m.RefuseActiveCompletePullState(); err != nil {
		t.Fatalf("retired complete-pull state error=%v", err)
	}
	dir := filepath.Join(m.Root, ".atl", "complete-pulls")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.RefuseActiveCompletePullState(); err != nil {
		t.Fatalf("empty complete-pull state error=%v", err)
	}
}

func TestReadJiraAttachmentBodyStageFileEnforcesPrivateBoundedInput(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "body.bin")
	if _, _, err := readJiraAttachmentBodyStageFile(root, path, -1); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("negative bound error=%v", err)
	}
	if data, found, err := readJiraAttachmentBodyStageFile(root, path, 3); err != nil || found || data != nil {
		t.Fatalf("missing stage data=%q found=%t err=%v", data, found, err)
	}
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readJiraAttachmentBodyStageFile(root, path, 3); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("permissive stage error=%v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readJiraAttachmentBodyStageFile(root, path, 2); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("oversized stage error=%v", err)
	}
	if data, found, err := readJiraAttachmentBodyStageFile(root, path, 3); err != nil || !found || string(data) != "one" {
		t.Fatalf("exact stage data=%q found=%t err=%v", data, found, err)
	}
}

func TestJiraAttachmentBodyMaterializationRejectsInvalidPublicationBeforeStaging(t *testing.T) {
	for _, tc := range []struct {
		name         string
		identity     string
		attachmentID string
		body         []byte
	}{
		{name: "invalid identity", identity: "0", attachmentID: "7", body: []byte("one")},
		{name: "invalid attachment", identity: "10001", attachmentID: "0", body: []byte("one")},
		{name: "unknown primary", identity: "10002", attachmentID: "7", body: []byte("one")},
		{name: "unknown attachment", identity: "10001", attachmentID: "99", body: []byte("one")},
		{name: "declared-size mismatch", identity: "10001", attachmentID: "7", body: []byte("mismatch")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := jiraAttachmentBodyMaterializationFixture(t, 1)
			if err := m.PublishJiraAttachmentBody(tc.identity, tc.attachmentID, tc.body); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("publication error=%v", err)
			}
			if _, err := os.Stat(m.jiraAttachmentBodyPublicationDir()); !os.IsNotExist(err) {
				t.Fatalf("invalid publication created stage: %v", err)
			}
		})
	}
}

func TestJiraAttachmentBodyMaterializationRetiresUnpublishedStageBeforeNextRead(t *testing.T) {
	m, state := jiraAttachmentBodyMaterializationFixture(t, 1)
	inspection, found, err := m.inspectJiraAttachmentBodyInventoryForState(state)
	if err != nil || !found {
		t.Fatalf("inspection found=%t err=%v", found, err)
	}
	_, nextData, err := nextJiraAttachmentBodySidecar(inspection, "7", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	dir := m.jiraAttachmentBodyPublicationDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	intent := jiraAttachmentBodyPublicationIntent{
		SchemaVersion: jiraAttachmentBodyPublicationSchema, Identity: "10001", State: state, AttachmentID: "7",
		SidecarSHA256: Hash(inspection.sidecarData), SidecarSize: int64(len(inspection.sidecarData)),
		NextSHA256: Hash(nextData), NextSize: int64(len(nextData)), BodySHA256: Hash([]byte("one")), BodySize: 3,
		Next: -1, WriteToken: completePullTestWriteToken,
	}
	if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverJiraAttachmentBodyMaterialization(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("unpublished stage remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments", "7.body")); !os.IsNotExist(err) {
		t.Fatalf("unpublished body appeared: %v", err)
	}
}

func TestJiraAttachmentBodyMaterializationRetiresPartiallyStagedPayloadBeforeNextRead(t *testing.T) {
	for _, residue := range []struct {
		name string
		file string
	}{
		{name: "body only", file: "body.bin"},
		{name: "successor sidecar only", file: "sidecar.json"},
	} {
		t.Run(residue.name, func(t *testing.T) {
			m, state := jiraAttachmentBodyMaterializationFixture(t, 1)
			inspection, found, err := m.inspectJiraAttachmentBodyInventoryForState(state)
			if err != nil || !found {
				t.Fatalf("inspection found=%t err=%v", found, err)
			}
			_, nextData, err := nextJiraAttachmentBodySidecar(inspection, "7", []byte("one"))
			if err != nil {
				t.Fatal(err)
			}
			dir := m.jiraAttachmentBodyPublicationDir()
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			intent := jiraAttachmentBodyPublicationIntent{
				SchemaVersion: jiraAttachmentBodyPublicationSchema, Identity: "10001", State: state, AttachmentID: "7",
				SidecarSHA256: Hash(inspection.sidecarData), SidecarSize: int64(len(inspection.sidecarData)),
				NextSHA256: Hash(nextData), NextSize: int64(len(nextData)), BodySHA256: Hash([]byte("one")), BodySize: 3,
				Next: -1, WriteToken: completePullTestWriteToken,
			}
			if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
				t.Fatal(err)
			}
			data := []byte("one")
			if residue.file == "sidecar.json" {
				data = nextData
			}
			if err := os.WriteFile(filepath.Join(dir, residue.file), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := m.RecoverJiraAttachmentBodyMaterialization(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("partial stage remains: %v", err)
			}
			if inventories, verifyErr := m.JiraAttachmentBodyInventories(); verifyErr != nil || len(inventories) != 1 || inventories[0].BodiesState != AttachmentBodiesNotRequested {
				t.Fatalf("inventories=%+v err=%v", inventories, verifyErr)
			}
		})
	}
}

func TestJiraAttachmentBodyMaterializationPromotesFullyStagedPayloadBeforeNextRead(t *testing.T) {
	m, state := jiraAttachmentBodyMaterializationFixture(t, 1)
	inspection, found, err := m.inspectJiraAttachmentBodyInventoryForState(state)
	if err != nil || !found {
		t.Fatalf("inspection found=%t err=%v", found, err)
	}
	next, nextData, err := nextJiraAttachmentBodySidecar(inspection, "7", []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	dir := m.jiraAttachmentBodyPublicationDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	intent := jiraAttachmentBodyPublicationIntent{
		SchemaVersion: jiraAttachmentBodyPublicationSchema, Identity: "10001", State: state, AttachmentID: "7",
		SidecarSHA256: Hash(inspection.sidecarData), SidecarSize: int64(len(inspection.sidecarData)),
		NextSHA256: Hash(nextData), NextSize: int64(len(nextData)), BodySHA256: Hash([]byte("one")), BodySize: 3,
		Next: -1, WriteToken: completePullTestWriteToken,
	}
	if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "body.bin"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sidecar.json"), nextData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverJiraAttachmentBodyMaterialization(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("fully staged transaction remains: %v", err)
	}
	if inventories, verifyErr := m.JiraAttachmentBodyInventories(); verifyErr != nil || len(inventories) != 1 || inventories[0].BodiesState != AttachmentBodiesComplete || next.BodiesState != AttachmentBodiesComplete {
		t.Fatalf("inventories=%+v err=%v", inventories, verifyErr)
	}
}

func TestJiraAttachmentBodyMaterializationRecoversDestinationWritesBeforeProgressMarker(t *testing.T) {
	for _, recovery := range []struct {
		name           string
		next           int
		writeSuccessor bool
	}{
		{name: "body written before next marker", next: 0},
		{name: "successor sidecar written before committed marker", next: 1, writeSuccessor: true},
	} {
		t.Run(recovery.name, func(t *testing.T) {
			m, state := jiraAttachmentBodyMaterializationFixture(t, 1)
			inspection, found, err := m.inspectJiraAttachmentBodyInventoryForState(state)
			if err != nil || !found {
				t.Fatalf("inspection found=%t err=%v", found, err)
			}
			next, nextData, err := nextJiraAttachmentBodySidecar(inspection, "7", []byte("one"))
			if err != nil {
				t.Fatal(err)
			}
			dir := m.jiraAttachmentBodyPublicationDir()
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			intent := jiraAttachmentBodyPublicationIntent{
				SchemaVersion: jiraAttachmentBodyPublicationSchema, Identity: "10001", State: state, AttachmentID: "7",
				SidecarSHA256: Hash(inspection.sidecarData), SidecarSize: int64(len(inspection.sidecarData)),
				NextSHA256: Hash(nextData), NextSize: int64(len(nextData)), BodySHA256: Hash([]byte("one")), BodySize: 3,
				Next: recovery.next, WriteToken: completePullTestWriteToken,
			}
			if err := m.saveJiraAttachmentBodyIntent(dir, intent); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "body.bin"), []byte("one"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sidecar.json"), nextData, 0o600); err != nil {
				t.Fatal(err)
			}
			bodyDir := filepath.Join(m.Root, "PROJ", "PROJ-1.attachments")
			if err := os.MkdirAll(bodyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(bodyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bodyDir, "7.body"), []byte("one"), 0o600); err != nil {
				t.Fatal(err)
			}
			if recovery.writeSuccessor {
				if err := os.WriteFile(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments.json"), nextData, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(m.Root, "PROJ", "PROJ-1.attachments.json"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.RecoverJiraAttachmentBodyMaterialization(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("recovered stage remains: %v", err)
			}
			if inventories, verifyErr := m.JiraAttachmentBodyInventories(); verifyErr != nil || len(inventories) != 1 || inventories[0].BodiesState != AttachmentBodiesComplete || next.BodiesState != AttachmentBodiesComplete {
				t.Fatalf("inventories=%+v err=%v", inventories, verifyErr)
			}
		})
	}
}

func TestJiraAttachmentBodyMaterializationRefusesChangedQualifiedEvidence(t *testing.T) {
	for _, mutation := range []struct {
		name  string
		apply func(t *testing.T, root string)
	}{
		{
			name: "captured body missing",
			apply: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, "PROJ", "PROJ-1.attachments", "7.body")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "captured body has permissive mode",
			apply: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Chmod(filepath.Join(root, "PROJ", "PROJ-1.attachments", "7.body"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "attachment directory has unowned member",
			apply: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "PROJ", "PROJ-1.attachments", "orphan.body"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "native substrate changed",
			apply: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "PROJ", "PROJ-1.wiki"), []byte("changed"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			m, _ := jiraAttachmentBodyMaterializationFixture(t, 1)
			if err := m.PublishJiraAttachmentBody("10001", "7", []byte("one")); err != nil {
				t.Fatal(err)
			}
			mutation.apply(t, m.Root)
			if _, err := m.JiraAttachmentBodyInventories(); !errors.Is(err, domain.ErrCheckFailed) {
				t.Fatalf("changed evidence error=%v", err)
			}
		})
	}
}

func TestJiraAttachmentBodyMaterializationRejectsActiveCompletePullState(t *testing.T) {
	m, _ := jiraAttachmentBodyMaterializationFixture(t, 0)
	dir := filepath.Join(m.Root, ".atl", "complete-pulls")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "active.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.RefuseActiveCompletePullState(); !errors.Is(err, domain.ErrCheckFailed) {
		t.Fatalf("active complete state error=%v", err)
	}
}

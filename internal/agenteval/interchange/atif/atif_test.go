package atif

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validEventSet() EventSet {
	return EventSet{
		Producer:       Producer{Name: "synthetic-agent", Version: "1.0.0"},
		ModelName:      "synthetic-model",
		DeclaredEvents: 2,
		Events: []Event{
			{StepID: 1, Role: RoleUser, State: StateStarted, Message: "owner-private task"},
			{
				StepID: 2, Role: RoleAgent, State: StateCompleted, Message: "owner-private result",
				ToolCalls: []ToolCall{{ToolCallID: "call-1", FunctionName: "fixture.read", Arguments: json.RawMessage(`{"limit":4,"path":"input.txt"}`)}},
				Results:   []ObservationResult{{SourceCallID: "call-1", Content: "owner-private output"}},
			},
		},
	}
}

func requireCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q", want)
	}
	got, ok := CodeOf(err)
	if !ok || got != want {
		t.Fatalf("error code = %q, %v; want %q", got, ok, want)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error %q does not unwrap to ErrInvalid", got)
	}
}

func mustProject(t *testing.T, input EventSet) Projection {
	t.Helper()
	projection, err := Project(input)
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	return projection
}

func mustEncode(t *testing.T, projection Projection) []byte {
	t.Helper()
	data, err := Encode(projection)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	return data
}

func TestProjectRoundTripAndBinding(t *testing.T) {
	input := validEventSet()
	projection := mustProject(t, input)
	if err := Validate(projection); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if projection.Document.SchemaVersion != ATIFSchemaVersion || projection.Document.Extra.Privacy != PrivacyOwnerPrivate ||
		projection.Document.Extra.Coverage != (Coverage{DeclaredEvents: 2, ProjectedSteps: 2, Complete: true}) ||
		!validDigest(projection.SourceSHA256) || projection.SourceSHA256 != projection.Document.Extra.SourceSHA256 ||
		projection.ProjectionSHA256 != projection.Document.Extra.ProjectionSHA256 {
		t.Fatalf("projection binding = %#v", projection)
	}
	encoded := mustEncode(t, projection)
	decoded, err := Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if roundTrip := mustEncode(t, decoded); !bytes.Equal(encoded, roundTrip) {
		t.Fatalf("round trip changed canonical bytes")
	}
	input.Events[1].ToolCalls[0].Arguments[0] = 'X'
	if bytes.Contains(mustEncode(t, projection), []byte("X")) {
		t.Fatal("projection retained caller-owned argument bytes")
	}
	for _, forbidden := range []string{"subagent_trajectories", "trajectory_path", "image", "public"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("closed owner-private subset emitted forbidden field/content %q", forbidden)
		}
	}
	if !bytes.Contains(encoded, []byte(`"privacy":"owner_private"`)) {
		t.Fatalf("privacy marker missing: %s", encoded)
	}
}

func TestProjectRejectsIncompleteEventsAndReferences(t *testing.T) {
	cases := []struct {
		name string
		edit func(*EventSet)
		code ErrorCode
	}{
		{name: "declared count drift", edit: func(input *EventSet) { input.DeclaredEvents++ }, code: ErrorInvalidEventSet},
		{name: "step gap", edit: func(input *EventSet) { input.Events[1].StepID = 3 }, code: ErrorInvalidEvent},
		{name: "user tool call", edit: func(input *EventSet) {
			input.Events[0].ToolCalls = []ToolCall{{ToolCallID: "call", FunctionName: "tool", Arguments: json.RawMessage(`{}`)}}
		}, code: ErrorInvalidToolCall},
		{name: "missing result", edit: func(input *EventSet) { input.Events[1].Results = nil }, code: ErrorInvalidObservation},
		{name: "unknown result reference", edit: func(input *EventSet) { input.Events[1].Results[0].SourceCallID = "missing" }, code: ErrorInvalidObservation},
		{name: "duplicate call id", edit: func(input *EventSet) {
			input.Events[1].ToolCalls = append(input.Events[1].ToolCalls, input.Events[1].ToolCalls[0])
		}, code: ErrorInvalidToolCall},
		{name: "stale source binding", edit: func(input *EventSet) { input.SourceSHA256 = strings.Repeat("a", 64) }, code: ErrorInvalidEventSet},
		{name: "noncanonical arguments", edit: func(input *EventSet) {
			input.Events[1].ToolCalls[0].Arguments = json.RawMessage(`{"path":"input.txt","limit":4}`)
		}, code: ErrorInvalidToolCall},
		{name: "duplicate argument member", edit: func(input *EventSet) {
			input.Events[1].ToolCalls[0].Arguments = json.RawMessage(`{"path":"a","path":"b"}`)
		}, code: ErrorInvalidToolCall},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := validEventSet()
			test.edit(&input)
			_, err := Project(input)
			requireCode(t, err, test.code)
		})
	}
}

func TestProjectRejectsCrossStepToolResultReferences(t *testing.T) {
	input := EventSet{
		Producer:       Producer{Name: "synthetic-agent", Version: "1.0.0"},
		DeclaredEvents: 2,
		Events: []Event{
			{StepID: 1, Role: RoleAgent, State: StateStarted, Message: "call", ToolCalls: []ToolCall{{ToolCallID: "call-1", FunctionName: "fixture.read", Arguments: json.RawMessage(`{}`)}}},
			{StepID: 2, Role: RoleAgent, State: StateCompleted, Message: "result", Results: []ObservationResult{{SourceCallID: "call-1", Content: "output"}}},
		},
	}
	requireCode(t, projectError(input), ErrorInvalidObservation)
}

func TestClosedWireRejectsUnknownDuplicateFutureAndHistoricalShapes(t *testing.T) {
	encoded := mustEncode(t, mustProject(t, validEventSet()))
	cases := map[string][]byte{
		"unknown root member":   bytes.Replace(encoded, []byte(`,"agent"`), []byte(`,"unknown":true,"agent"`), 1),
		"duplicate root member": bytes.Replace(encoded, []byte(`,"agent"`), []byte(`,"agent":{},"agent"`), 1),
		"future generation":     bytes.Replace(encoded, []byte(`"schema_version":"ATIF-v1.7"`), []byte(`"schema_version":"ATIF-v1.8"`), 1),
		"historical generation": bytes.Replace(encoded, []byte(`"schema_version":"ATIF-v1.7"`), []byte(`"schema_version":"ATIF-v1.6"`), 1),
		"missing LF":            encoded[:len(encoded)-1],
		"trailing bytes":        append(append([]byte{}, encoded...), 'x'),
		"leading whitespace":    append([]byte{' '}, encoded...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) { requireCode(t, decodeBytes(data), ErrorInvalidWire) })
	}
}

func decodeBytes(data []byte) error {
	_, err := Decode(bytes.NewReader(data))
	return err
}

func TestProjectionTamperAndBoundsFailClosed(t *testing.T) {
	projection := mustProject(t, validEventSet())
	missingWrapperDigest := projection
	missingWrapperDigest.SourceSHA256 = ""
	requireCode(t, Validate(missingWrapperDigest), ErrorInvalidBinding)
	projection.Document.Extra.ProjectionSHA256 = strings.Repeat("b", 64)
	requireCode(t, Validate(projection), ErrorInvalidProjection)

	tooMany := validEventSet()
	tooMany.DeclaredEvents = MaxSteps + 1
	tooMany.Events = make([]Event, MaxSteps+1)
	requireCode(t, projectError(tooMany), ErrorInvalidEventSet)

	tooManyCalls := validEventSet()
	tooManyCalls.Events[1].ToolCalls = make([]ToolCall, MaxToolCalls+1)
	requireCode(t, projectError(tooManyCalls), ErrorLimitExceeded)

	tooLargeMessage := validEventSet()
	tooLargeMessage.Events[0].Message = strings.Repeat("x", MaxTextBytes+1)
	requireCode(t, projectError(tooLargeMessage), ErrorInvalidEvent)

	tooLargeArgument := validEventSet()
	tooLargeArgument.Events[1].ToolCalls[0].Arguments = json.RawMessage(`{"value":"` + strings.Repeat("x", MaxArgumentBytes) + `"}`)
	requireCode(t, projectError(tooLargeArgument), ErrorInvalidToolCall)

	tooDeepArgument := validEventSet()
	tooDeepArgument.Events[1].ToolCalls[0].Arguments = json.RawMessage(strings.Repeat(`{"a":`, MaxJSONDepth+2) + `0` + strings.Repeat(`}`, MaxJSONDepth+2))
	requireCode(t, projectError(tooDeepArgument), ErrorInvalidToolCall)

	largeDocument := EventSet{Producer: Producer{Name: "synthetic-agent", Version: "1.0.0"}, DeclaredEvents: 20, Events: make([]Event, 20)}
	for index := range largeDocument.Events {
		largeDocument.Events[index] = Event{StepID: uint32(index + 1), Role: RoleUser, State: StateStarted, Message: strings.Repeat("x", MaxTextBytes)}
	}
	requireCode(t, projectError(largeDocument), ErrorLimitExceeded)
	wireOversized := EventSet{Producer: largeDocument.Producer, DeclaredEvents: 16, Events: make([]Event, 16)}
	for index := range wireOversized.Events {
		wireOversized.Events[index] = Event{StepID: uint32(index + 1), Role: RoleUser, State: StateStarted, Message: strings.Repeat("x", MaxTextBytes)}
	}
	requireCode(t, projectError(wireOversized), ErrorLimitExceeded)
}

func projectError(input EventSet) error {
	_, err := Project(input)
	return err
}

func TestExportOwnerPrivateGuardsDestinationAndMode(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	root := filepath.Join(base, "private")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	projection := mustProject(t, validEventSet())
	request := ExportRequest{OwnerPrivateRoot: root, RepositoryRoot: repository, RelativePath: "nested/trajectory.json", Projection: projection}
	if err := ExportOwnerPrivate(request); err != nil {
		t.Fatalf("ExportOwnerPrivate() error = %v", err)
	}
	path := filepath.Join(root, "nested", "trajectory.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, mustEncode(t, projection)) {
		t.Fatal("exported bytes differ from canonical projection")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("exported mode = %v, err=%v", info.Mode(), err)
	}
	requireCode(t, ExportOwnerPrivate(request), ErrorInvalidDestination)

	unsafeRoot := filepath.Join(base, "unsafe-root")
	if err := os.Mkdir(unsafeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	request.OwnerPrivateRoot = unsafeRoot
	requireCode(t, ExportOwnerPrivate(request), ErrorInvalidDestination)

	insideRepository := filepath.Join(repository, "private")
	if err := os.Mkdir(insideRepository, 0o700); err != nil {
		t.Fatal(err)
	}
	request.OwnerPrivateRoot = insideRepository
	requireCode(t, ExportOwnerPrivate(request), ErrorInvalidDestination)

	request.OwnerPrivateRoot = root
	request.RelativePath = "../escape.json"
	requireCode(t, ExportOwnerPrivate(request), ErrorInvalidDestination)
	request.RelativePath = filepath.Join("nested", "trajectory-2.json")
	unsafeParent := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafeParent, 0o755); err != nil {
		t.Fatal(err)
	}
	request.RelativePath = "unsafe/trajectory-2.json"
	requireCode(t, ExportOwnerPrivate(request), ErrorInvalidDestination)
	rootLink := filepath.Join(base, "root-link")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	request.OwnerPrivateRoot = rootLink
	request.RelativePath = "trajectory-3.json"
	requireCode(t, ExportOwnerPrivate(request), ErrorInvalidDestination)
	request.OwnerPrivateRoot = root
	if err := os.Symlink(base, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	request.RelativePath = "link/escape.json"
	requireCode(t, ExportOwnerPrivate(request), ErrorInvalidDestination)
}

func TestExportRejectsRepositoryReplacementBeforePublish(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	root := filepath.Join(base, "private")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	projection := mustProject(t, validEventSet())
	request := ExportRequest{OwnerPrivateRoot: root, RepositoryRoot: repository, RelativePath: "trajectory.json", Projection: projection}
	previousHook := exportHook
	defer func() { exportHook = previousHook }()
	exportHook = func(point exportHookPoint) {
		if point != exportBeforePublish {
			return
		}
		replaced := repository + ".replaced"
		if err := os.Rename(repository, replaced); err != nil {
			t.Fatalf("rename repository: %v", err)
		}
		if err := os.Mkdir(repository, 0o755); err != nil {
			t.Fatalf("replace repository: %v", err)
		}
	}
	requireCode(t, ExportOwnerPrivate(request), ErrorExportFailed)
	if _, err := os.Lstat(filepath.Join(root, "trajectory.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository replacement left final output, err=%v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("repository replacement leaked owner-private temporary entries: %v", entries)
	}
}

func TestExportRejectsFinalInodeReplacement(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	root := filepath.Join(base, "private")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	projection := mustProject(t, validEventSet())
	request := ExportRequest{OwnerPrivateRoot: root, RepositoryRoot: repository, RelativePath: "trajectory.json", Projection: projection}
	previousHook := exportHook
	defer func() { exportHook = previousHook }()
	exportHook = func(point exportHookPoint) {
		if point != exportAfterPublish {
			return
		}
		path := filepath.Join(root, "trajectory.json")
		replacement := filepath.Join(base, "replacement")
		if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
			t.Fatalf("replace final write: %v", err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatalf("replace final rename: %v", err)
		}
	}
	requireCode(t, ExportOwnerPrivate(request), ErrorExportFailed)
}

func TestExportRetriesTransientTemporaryCleanupFailure(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	root := filepath.Join(base, "private")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	projection := mustProject(t, validEventSet())
	request := ExportRequest{OwnerPrivateRoot: root, RepositoryRoot: repository, RelativePath: "trajectory.json", Projection: projection}
	previousRemove := exportTemporaryRemove
	defer func() { exportTemporaryRemove = previousRemove }()
	var calls int
	exportTemporaryRemove = func(ownerRoot *os.Root, name string) error {
		calls++
		if calls == 1 {
			return errors.New("injected temporary cleanup failure")
		}
		return ownerRoot.Remove(name)
	}
	if err := ExportOwnerPrivate(request); err != nil {
		t.Fatalf("ExportOwnerPrivate() error after cleanup retry = %v", err)
	}
	if calls < 2 {
		t.Fatalf("temporary cleanup calls = %d, want retry after publish failure", calls)
	}
	if _, err := os.Lstat(filepath.Join(root, "trajectory.json")); err != nil {
		t.Fatalf("transient cleanup failure lost final output, err=%v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "trajectory.json" {
		t.Fatalf("transient cleanup state entries = %v, want final only", entries)
	}
}

func TestExportReportsCommittedWhenTemporaryCleanupPersists(t *testing.T) {
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	root := filepath.Join(base, "private")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	projection := mustProject(t, validEventSet())
	request := ExportRequest{OwnerPrivateRoot: root, RepositoryRoot: repository, RelativePath: "trajectory.json", Projection: projection}
	previousRemove := exportTemporaryRemove
	defer func() { exportTemporaryRemove = previousRemove }()
	var calls int
	exportTemporaryRemove = func(*os.Root, string) error {
		calls++
		return errors.New("persistent temporary cleanup failure")
	}
	requireCode(t, ExportOwnerPrivate(request), ErrorExportCommitted)
	if calls != 2 {
		t.Fatalf("temporary cleanup calls = %d, want bounded retry", calls)
	}
	finalPath := filepath.Join(root, "trajectory.json")
	data, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("committed final output missing: %v", err)
	}
	if !bytes.Equal(data, mustEncode(t, projection)) {
		t.Fatal("committed final output differs from canonical projection")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("persistent cleanup state entries = %d, want final plus one private recovery link", len(entries))
	}
}

func FuzzDecodeRejectsUntrustedBytes(f *testing.F) {
	projection, err := Project(validEventSet())
	if err != nil {
		f.Fatalf("Project() error = %v", err)
	}
	seed, err := Encode(projection)
	if err != nil {
		f.Fatalf("Encode() error = %v", err)
	}
	f.Add(seed)
	f.Add([]byte(`{"schema_version":"ATIF-v1.7","agent":{},"steps":[],"extra":{}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		decoded, err := Decode(bytes.NewReader(data))
		if err != nil {
			return
		}
		canonical, encodeErr := Encode(decoded)
		if encodeErr != nil || !bytes.Equal(data, canonical) {
			t.Fatalf("accepted bytes are not canonical: %v", encodeErr)
		}
	})
}

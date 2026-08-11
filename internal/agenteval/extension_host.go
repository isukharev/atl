package agenteval

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
	"sort"
	"time"

	"github.com/isukharev/atl/internal/agenteval/extension"
)

const (
	ExtensionConformanceBundleSchema        = "agent-eval/extension-conformance-bundle"
	ExtensionConformanceReportSchema        = "agent-eval/extension-conformance-report"
	ExtensionConformanceBundleSchemaVersion = 1
	ExtensionConformanceReportSchemaVersion = 1
	extensionConformanceMaxBytes            = 1 << 20
	extensionConformanceScope               = "extension_protocol"
	extensionExpectedResult                 = "result"
	extensionExpectedError                  = "error"
	extensionExpectedCanceled               = "canceled"
)

var (
	errExtensionInvalidBundle     = errors.New("extension_invalid_bundle")
	errExtensionInvalidReport     = errors.New("extension_invalid_report")
	errExtensionUnsupportedPolicy = errors.New("extension_unsupported_policy")
	errExtensionCompatibility     = errors.New("extension_compatibility_error")
	errExtensionConformanceFailed = errors.New("extension_conformance_failed")
	errExtensionOutcomeUnknown    = errors.New("extension_outcome_unknown")
)

// ExtensionConformanceBundle is an exact, content-addressed set of synthetic
// cases for one single-role manifest and executable.
type ExtensionConformanceBundle struct {
	Schema           string                     `json:"schema"`
	SchemaVersion    int                        `json:"schema_version"`
	ContractVersion  string                     `json:"contract_version"`
	ContractSHA256   string                     `json:"contract_sha256"`
	ProtocolVersion  int                        `json:"protocol_version"`
	ProtocolSHA256   string                     `json:"protocol_sha256"`
	ManifestSHA256   string                     `json:"manifest_sha256"`
	ExecutableSHA256 string                     `json:"executable_sha256"`
	Cases            []ExtensionConformanceCase `json:"cases"`
}

type ExtensionConformanceCase struct {
	ID                   string                         `json:"id"`
	Role                 extension.Role                 `json:"role"`
	Operation            extension.Operation            `json:"operation"`
	Configuration        []extension.ConfigurationValue `json:"configuration"`
	Inputs               []extension.ArtifactReference  `json:"inputs"`
	Policy               extension.InvocationPolicy     `json:"policy"`
	DeadlineMilliseconds int64                          `json:"deadline_milliseconds"`
	Expected             ExtensionConformanceExpected   `json:"expected"`
}

type ExtensionConformanceExpected struct {
	Type    string                        `json:"type"`
	Outputs []extension.ArtifactReference `json:"outputs,omitempty"`
	Error   extension.ComponentErrorCode  `json:"error,omitempty"`
}

// ExtensionConformanceReport contains only structural identities and closed
// outcomes. Paths, arguments, environment, sessions, attempts, stderr, and
// artifact bodies are absent by type.
type ExtensionConformanceReport struct {
	Schema             string                           `json:"schema"`
	SchemaVersion      int                              `json:"schema_version"`
	Scope              string                           `json:"scope"`
	ContractVersion    string                           `json:"contract_version"`
	ContractSHA256     string                           `json:"contract_sha256"`
	ProtocolVersion    int                              `json:"protocol_version"`
	ProtocolSHA256     string                           `json:"protocol_sha256"`
	BundleSHA256       string                           `json:"bundle_sha256"`
	ManifestSHA256     string                           `json:"manifest_sha256"`
	ExecutableSHA256   string                           `json:"executable_sha256"`
	ComponentID        string                           `json:"component_id"`
	ComponentVersion   string                           `json:"component_version"`
	Role               extension.Role                   `json:"role"`
	Capabilities       []extension.CapabilityClaim      `json:"capabilities"`
	CleanupAssurance   string                           `json:"cleanup_assurance"`
	Cases              []ExtensionConformanceCaseReport `json:"cases"`
	ProtocolConformant bool                             `json:"protocol_conformant"`
}

func verifyExtensionProtocol(
	ctx context.Context,
	manifestData []byte,
	executablePath string,
	arguments []string,
	bundleData []byte,
	attemptStore *AttemptLedgerStore, adapterContractDigest string,
) (ExtensionConformanceReport, error) {
	verificationCtx, cancelVerification, err := extensionVerificationContext(ctx)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	defer cancelVerification()
	ctx = verificationCtx
	if err := ctx.Err(); err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	manifest, err := extension.DecodeManifest(manifestData)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	bundle, err := DecodeExtensionConformanceBundle(bundleData)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	manifestDigest := sha256HexBytes(manifestData)
	bundleDigest := sha256HexBytes(bundleData)
	if bundle.ContractVersion != extension.ContractVersion || bundle.ContractSHA256 != extension.ContractSHA256() ||
		bundle.ProtocolVersion != extension.ProtocolVersion || bundle.ProtocolSHA256 != extension.ProtocolSHA256() ||
		bundle.ManifestSHA256 != manifestDigest || bundle.ExecutableSHA256 != manifest.ExecutableSHA256 {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	if !extensionHostSupports(manifest.Requirements) || !extensionPlatformMatches(manifest.Platforms) {
		return ExtensionConformanceReport{}, errExtensionUnsupportedPolicy
	}
	if err := validateExtensionBundleAgainstManifest(bundle, manifest); err != nil {
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	report := ExtensionConformanceReport{
		Schema: ExtensionConformanceReportSchema, SchemaVersion: ExtensionConformanceReportSchemaVersion,
		Scope: extensionConformanceScope, ContractVersion: extension.ContractVersion,
		ContractSHA256: extension.ContractSHA256(), ProtocolVersion: extension.ProtocolVersion,
		ProtocolSHA256: extension.ProtocolSHA256(), BundleSHA256: bundleDigest,
		ManifestSHA256: manifestDigest, ExecutableSHA256: manifest.ExecutableSHA256,
		ComponentID: manifest.Component.ID, ComponentVersion: manifest.Component.Version,
		Role:             manifest.Component.Role,
		Capabilities:     append([]extension.CapabilityClaim(nil), manifest.Component.Capabilities...),
		CleanupAssurance: extensionCleanupAssurance(),
		Cases:            make([]ExtensionConformanceCaseReport, 0, len(bundle.Cases)),
	}
	attemptSessions, closePlannedAttempts, err := prepareExtensionProtocolAttempts(
		attemptStore, manifest, bundle, manifestDigest, bundleDigest, adapterContractDigest)
	if err != nil {
		return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
	}
	defer closePlannedAttempts()
	bundleInvocationEntered := false
	for caseIndex, testCase := range bundle.Cases {
		caseCtx, cancelCase, err := extensionContextDeadline(ctx, time.Duration(testCase.DeadlineMilliseconds)*time.Millisecond)
		if err != nil {
			if bundleInvocationEntered {
				return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
			}
			return ExtensionConformanceReport{}, errExtensionCompatibility
		}
		if err := caseCtx.Err(); err != nil {
			cancelCase()
			if bundleInvocationEntered {
				return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
			}
			return ExtensionConformanceReport{}, errExtensionCompatibility
		}
		admitted, err := admitExtensionExecutable(executablePath, manifest.ExecutableSHA256)
		if err != nil {
			cancelCase()
			if bundleInvocationEntered || errors.Is(err, errExtensionAdmissionCleanup) {
				return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
			}
			return ExtensionConformanceReport{}, errExtensionCompatibility
		}
		if err := caseCtx.Err(); err != nil {
			removeErr := admitted.remove()
			cancelCase()
			if bundleInvocationEntered || removeErr != nil {
				return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
			}
			return ExtensionConformanceReport{}, errExtensionCompatibility
		}
		attempt, attemptErr := beginExtensionProtocolAttempt(attemptSessions, caseIndex)
		if attemptErr != nil {
			_ = admitted.remove()
			cancelCase()
			return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
		}
		caseReport, assurance, err := runExtensionConformanceCase(caseCtx, admitted, arguments, manifest, testCase, attempt)
		removeErr := admitted.remove()
		cancelCase()
		if attempt != nil {
			if lifecycleErr := finalizeExtensionAttempt(attempt, testCase, caseReport, assurance, err, removeErr); lifecycleErr != nil {
				return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
			}
		}
		if removeErr != nil {
			return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
		}
		if err != nil {
			if bundleInvocationEntered || errors.Is(err, errExtensionOutcomeUnknown) {
				return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
			}
			return ExtensionConformanceReport{}, err
		}
		bundleInvocationEntered = true
		if assurance != report.CleanupAssurance {
			return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
		}
		report.Cases = append(report.Cases, caseReport)
	}
	if err := ctx.Err(); err != nil {
		if bundleInvocationEntered {
			return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
		}
		return ExtensionConformanceReport{}, errExtensionCompatibility
	}
	report.ProtocolConformant = true
	if err := ValidateExtensionConformanceReport(report); err != nil {
		return ExtensionConformanceReport{}, errExtensionOutcomeUnknown
	}
	return report, nil
}

func runExtensionConformanceCase(
	parent context.Context,
	admitted admittedExtensionExecutable,
	arguments []string,
	manifest extension.Manifest,
	testCase ExtensionConformanceCase,
	attempt *DurableAttemptSession,
) (ExtensionConformanceCaseReport, string, error) {
	if err := parent.Err(); err != nil {
		return ExtensionConformanceCaseReport{}, "", errExtensionCompatibility
	}
	ctx := parent
	sessionID, err := randomExtensionIdentity()
	if err != nil {
		return ExtensionConformanceCaseReport{}, "", errExtensionCompatibility
	}
	attemptID := ""
	if attempt != nil {
		attemptID = attempt.plan.AttemptID
	} else {
		attemptID, err = randomExtensionIdentity()
		if err != nil {
			return ExtensionConformanceCaseReport{}, "", errExtensionCompatibility
		}
	}
	if err := ctx.Err(); err != nil {
		return ExtensionConformanceCaseReport{}, "", errExtensionCompatibility
	}
	initialize, err := extension.NewInitialize(manifest, sessionID, attemptID)
	if err != nil {
		return ExtensionConformanceCaseReport{}, "", errExtensionCompatibility
	}
	if err := ctx.Err(); err != nil {
		return ExtensionConformanceCaseReport{}, "", errExtensionCompatibility
	}
	process, err := startExtensionProcessWithSession(admitted, arguments, attempt)
	if err != nil {
		var startError *extensionProcessStartError
		if errors.As(err, &startError) && startError.possibleEntry {
			return ExtensionConformanceCaseReport{}, extensionCleanupAssurance(), errExtensionOutcomeUnknown
		}
		return ExtensionConformanceCaseReport{}, "", errExtensionCompatibility
	}
	invocationMayHaveEntered := false
	cancelSent := false
	var invoke extension.Frame
	cleanup := extensionProcessCleanup{assurance: extensionCleanupAssurance()}
	defer func() {
		if cleanup.assurance == extensionCleanupAssurance() && !cleanup.complete {
			cleanup = process.cleanup(extensionCancelGrace)
		}
	}()

	fail := func(cause error) (ExtensionConformanceCaseReport, string, error) {
		if invocationMayHaveEntered && !cancelSent &&
			(errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)) {
			if cancelFrame, cancelErr := extension.NewCancel(invoke); cancelErr == nil {
				if encoded, encodeErr := extension.EncodeFrame(cancelFrame); encodeErr == nil {
					cancelCtx, cancelWrite := context.WithTimeout(context.Background(), extensionCancelGrace)
					_ = process.writeFrame(cancelCtx, encoded)
					cancelWrite()
					clear(encoded)
				}
			}
		}
		cleanup = process.cleanup(extensionCancelGrace)
		return ExtensionConformanceCaseReport{}, cleanup.assurance, errExtensionOutcomeUnknown
	}

	initializeData, err := extension.EncodeFrame(initialize)
	if err != nil {
		return fail(err)
	}
	writeErr := process.writeFrame(ctx, initializeData)
	clear(initializeData)
	if writeErr != nil {
		return fail(writeErr)
	}
	initializedData, err := process.readFrame(ctx)
	if err != nil {
		return fail(err)
	}
	initialized, err := extension.DecodeFrame(initializedData)
	clear(initializedData)
	if err != nil {
		return fail(err)
	}
	if err := extension.ValidateInitialized(manifest, initialize, initialized); err != nil {
		return fail(err)
	}
	invocationID, err := randomExtensionIdentity()
	if err != nil {
		return fail(err)
	}
	invoke, err = newExtensionCaseInvoke(manifest, initialize, initialized, invocationID, testCase)
	if err != nil {
		return fail(err)
	}
	invokeData, err := extension.EncodeFrame(invoke)
	if err != nil {
		return fail(err)
	}
	invocationMayHaveEntered = true
	writeErr = process.writeFrame(ctx, invokeData)
	clear(invokeData)
	if writeErr != nil {
		return fail(writeErr)
	}
	var cancelFrame extension.Frame
	if testCase.Expected.Type == extensionExpectedCanceled {
		cancelFrame, err = extension.NewCancel(invoke)
		if err != nil {
			return fail(err)
		}
		cancelData, encodeErr := extension.EncodeFrame(cancelFrame)
		if encodeErr != nil {
			return fail(encodeErr)
		}
		cancelSent = true
		writeErr = process.writeFrame(ctx, cancelData)
		clear(cancelData)
		if writeErr != nil {
			return fail(writeErr)
		}
	}
	terminalData, err := process.readFrame(ctx)
	if err != nil {
		return fail(err)
	}
	terminal, err := extension.DecodeFrame(terminalData)
	clear(terminalData)
	if err != nil {
		return fail(err)
	}
	terminalValid := extension.ValidateTerminal(manifest, invoke, terminal) == nil
	if testCase.Expected.Type == extensionExpectedCanceled {
		terminalValid = extension.ValidateCanceled(cancelFrame, terminal) == nil
	}
	if !terminalValid || !extensionTerminalMatches(testCase.Expected, terminal) {
		return fail(errExtensionConformanceFailed)
	}
	if err := process.closeStdin(); err != nil {
		return fail(err)
	}
	if extra, err := process.readFrame(ctx); !errors.Is(err, io.EOF) {
		clear(extra)
		return fail(errExtensionConformanceFailed)
	}
	select {
	case <-process.waitDone:
	case <-ctx.Done():
		return fail(ctx.Err())
	}
	if process.waitErr != nil || process.stderr.didOverflow() {
		return fail(errExtensionConformanceFailed)
	}
	cleanup = process.cleanup(0)
	if cleanup.err != nil || !cleanup.complete {
		return ExtensionConformanceCaseReport{}, cleanup.assurance, errExtensionOutcomeUnknown
	}
	terminalType := extensionExpectedResult
	switch terminal.Type {
	case extension.MessageError:
		terminalType = extensionExpectedError
	case extension.MessageCanceled:
		terminalType = extensionExpectedCanceled
	}
	return ExtensionConformanceCaseReport{
		ID: testCase.ID, Operation: testCase.Operation, Terminal: terminalType, Status: "passed",
	}, cleanup.assurance, nil
}

func extensionTerminalMatches(expected ExtensionConformanceExpected, terminal extension.Frame) bool {
	switch expected.Type {
	case extensionExpectedResult:
		return terminal.Type == extension.MessageResult && slices.Equal(terminal.Result.Outputs, expected.Outputs)
	case extensionExpectedError:
		return terminal.Type == extension.MessageError && terminal.Error.Code == expected.Error
	case extensionExpectedCanceled:
		return terminal.Type == extension.MessageCanceled
	default:
		return false
	}
}

func randomExtensionIdentity() (string, error) {
	data := make([]byte, extension.SHA256HexCharacters/2)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	identity := hex.EncodeToString(data)
	clear(data)
	return identity, nil
}

func extensionHostSupports(requirements []extension.EnforcementRequirement) bool {
	for _, requirement := range requirements {
		switch requirement {
		case extension.EnforcementExactEnvironment, extension.EnforcementPrivateWorkingDirectory,
			extension.EnforcementBoundedIO, extension.EnforcementDeadline, extension.EnforcementBestEffortProcessGroup:
		default:
			return false
		}
	}
	return true
}

func extensionPlatformMatches(platforms []extension.Platform) bool {
	for _, platform := range platforms {
		if platform.OS == runtime.GOOS && platform.Architecture == runtime.GOARCH {
			return true
		}
	}
	return false
}

func validateExtensionBundleAgainstManifest(bundle ExtensionConformanceBundle, manifest extension.Manifest) error {
	wantOperations := make([]extension.Operation, 0, len(manifest.Component.Capabilities))
	for _, operation := range manifest.Component.Operations {
		if extension.OperationSupported(manifest, operation) {
			wantOperations = append(wantOperations, operation)
		}
	}
	initialize, err := extension.NewInitialize(manifest, stringsRepeatHex('a'), stringsRepeatHex('b'))
	if err != nil {
		return errExtensionCompatibility
	}
	initialized, err := extension.NewInitialized(manifest, initialize)
	if err != nil {
		return errExtensionCompatibility
	}
	gotOperations := make([]extension.Operation, 0, len(bundle.Cases))
	cancellationCases := 0
	for _, testCase := range bundle.Cases {
		if testCase.Role != manifest.Component.Role || !extension.OperationSupported(manifest, testCase.Operation) {
			return errExtensionCompatibility
		}
		invoke, invokeErr := newExtensionCaseInvoke(manifest, initialize, initialized, stringsRepeatHex('f'), testCase)
		if invokeErr != nil || !validExpectedExtensionTerminal(manifest, invoke, testCase.Expected) {
			return errExtensionCompatibility
		}
		if testCase.Expected.Type == extensionExpectedCanceled {
			cancellationCases++
		} else {
			gotOperations = append(gotOperations, testCase.Operation)
		}
	}
	sort.Slice(gotOperations, func(i, j int) bool { return gotOperations[i] < gotOperations[j] })
	if cancellationCases != 1 || !slices.Equal(gotOperations, wantOperations) {
		return errExtensionCompatibility
	}
	return nil
}

// EncodeExtensionConformanceBundle emits canonical compact JSON plus LF.
func EncodeExtensionConformanceBundle(value ExtensionConformanceBundle) ([]byte, error) {
	if err := ValidateExtensionConformanceBundle(value); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > extensionConformanceMaxBytes {
		return nil, errExtensionInvalidBundle
	}
	return append(data, '\n'), nil
}

func DecodeExtensionConformanceBundle(data []byte) (ExtensionConformanceBundle, error) {
	var value ExtensionConformanceBundle
	if err := decodeCanonicalExtensionDocument(data, extensionConformanceMaxBytes, &value); err != nil ||
		ValidateExtensionConformanceBundle(value) != nil {
		return ExtensionConformanceBundle{}, errExtensionInvalidBundle
	}
	encoded, err := EncodeExtensionConformanceBundle(value)
	if err != nil || !bytes.Equal(encoded, data) {
		return ExtensionConformanceBundle{}, errExtensionInvalidBundle
	}
	return value, nil
}

func ValidateExtensionConformanceBundle(value ExtensionConformanceBundle) error {
	if value.Schema != ExtensionConformanceBundleSchema || value.SchemaVersion != ExtensionConformanceBundleSchemaVersion ||
		value.ContractVersion != extension.ContractVersion || value.ContractSHA256 != extension.ContractSHA256() ||
		value.ProtocolVersion != extension.ProtocolVersion || value.ProtocolSHA256 != extension.ProtocolSHA256() ||
		!validSHA256(value.ManifestSHA256) || !validSHA256(value.ExecutableSHA256) || value.Cases == nil ||
		len(value.Cases) == 0 || len(value.Cases) > extension.MaxCollectionEntries {
		return errExtensionInvalidBundle
	}
	previous := ""
	for _, testCase := range value.Cases {
		if !validStandaloneExtensionID(testCase.ID) || testCase.ID <= previous ||
			testCase.DeadlineMilliseconds < 1 || testCase.DeadlineMilliseconds > extension.MaxDeadlineMilliseconds ||
			extension.CapabilityFor(testCase.Role, testCase.Operation) == "" || !validExtensionCaseShape(testCase) {
			return errExtensionInvalidBundle
		}
		previous = testCase.ID
	}
	return nil
}

func validExtensionCaseShape(testCase ExtensionConformanceCase) bool {
	if testCase.Configuration == nil || testCase.Inputs == nil ||
		testCase.Policy.OutputPrivacy != extension.PrivacyPublic {
		return false
	}
	for _, input := range testCase.Inputs {
		if input.Privacy != extension.PrivacyPublic {
			return false
		}
	}
	for _, output := range testCase.Expected.Outputs {
		if output.Privacy != extension.PrivacyPublic {
			return false
		}
	}
	manifest := extensionCaseProbeManifest(testCase)
	initialize, err := extension.NewInitialize(manifest, stringsRepeatHex('a'), stringsRepeatHex('b'))
	if err != nil {
		return false
	}
	initialized, err := extension.NewInitialized(manifest, initialize)
	if err != nil {
		return false
	}
	invoke, err := newExtensionCaseInvoke(manifest, initialize, initialized, stringsRepeatHex('f'), testCase)
	return err == nil && validExpectedExtensionTerminal(manifest, invoke, testCase.Expected)
}

func newExtensionCaseInvoke(
	manifest extension.Manifest,
	initialize, initialized extension.Frame,
	invocationID string,
	testCase ExtensionConformanceCase,
) (extension.Frame, error) {
	if testCase.Expected.Type == extensionExpectedCanceled {
		return extension.NewCancellationProbeInvoke(
			manifest, initialize, initialized, invocationID, testCase.Operation,
			testCase.Configuration, testCase.Inputs, testCase.Policy,
		)
	}
	return extension.NewInvoke(
		manifest, initialize, initialized, invocationID, testCase.Operation,
		testCase.Configuration, testCase.Inputs, testCase.Policy,
	)
}

func extensionCaseProbeManifest(testCase ExtensionConformanceCase) extension.Manifest {
	configuration := make([]extension.ConfigurationField, len(testCase.Configuration))
	for index, value := range testCase.Configuration {
		field := extension.ConfigurationField{Name: value.Name, Required: true}
		switch {
		case value.Boolean != nil:
			field.Kind = extension.ConfigurationBoolean
		case value.Integer != nil:
			field.Kind = extension.ConfigurationInteger
			minimum, maximum := *value.Integer, *value.Integer
			field.Minimum, field.Maximum = &minimum, &maximum
		case value.Enum != "":
			field.Kind = extension.ConfigurationEnum
			field.Values = []string{value.Enum}
		}
		configuration[index] = field
	}
	return extension.Manifest{
		Schema: extension.ManifestSchema, SchemaVersion: extension.ManifestSchemaVersion,
		ContractVersion: extension.ContractVersion, ProtocolVersions: []int{extension.ProtocolVersion},
		Component: extension.Descriptor{ID: "synthetic-component", Version: "1", Role: testCase.Role,
			Operations: extension.OperationsForRole(testCase.Role), Capabilities: supportedExtensionClaims(testCase.Role)},
		ExecutableSHA256: stringsRepeatHex('c'), ConfigurationSchema: configuration,
		Platforms:    []extension.Platform{{OS: "linux", Architecture: "amd64"}},
		Requirements: []extension.EnforcementRequirement{extension.EnforcementBoundedIO},
	}
}

func validExpectedExtensionTerminal(
	manifest extension.Manifest,
	invoke extension.Frame,
	expected ExtensionConformanceExpected,
) bool {
	var terminal extension.Frame
	var err error
	switch expected.Type {
	case extensionExpectedResult:
		if expected.Error != "" {
			return false
		}
		terminal, err = extension.NewResult(invoke, expected.Outputs)
	case extensionExpectedError:
		if len(expected.Outputs) != 0 {
			return false
		}
		terminal, err = extension.NewComponentError(invoke, expected.Error)
	case extensionExpectedCanceled:
		if len(expected.Outputs) != 0 || expected.Error != "" {
			return false
		}
		cancel, cancelErr := extension.NewCancel(invoke)
		if cancelErr != nil {
			return false
		}
		terminal, err = extension.NewCanceled(cancel)
		return err == nil && extension.ValidateCanceled(cancel, terminal) == nil
	default:
		return false
	}
	return err == nil && extension.ValidateTerminal(manifest, invoke, terminal) == nil
}

func supportedExtensionClaims(role extension.Role) []extension.CapabilityClaim {
	operations := extension.OperationsForRole(role)
	claims := make([]extension.CapabilityClaim, len(operations))
	for index, operation := range operations {
		claims[index] = extension.CapabilityClaim{ID: extension.CapabilityFor(role, operation), State: extension.CapabilitySupported}
	}
	return claims
}

func EncodeExtensionConformanceReport(value ExtensionConformanceReport) ([]byte, error) {
	if err := ValidateExtensionConformanceReport(value); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > extensionConformanceMaxBytes {
		return nil, errExtensionInvalidReport
	}
	return append(data, '\n'), nil
}

func DecodeExtensionConformanceReport(data []byte) (ExtensionConformanceReport, error) {
	var value ExtensionConformanceReport
	if err := decodeCanonicalExtensionDocument(data, extensionConformanceMaxBytes, &value); err != nil ||
		ValidateExtensionConformanceReport(value) != nil {
		return ExtensionConformanceReport{}, errExtensionInvalidReport
	}
	encoded, err := EncodeExtensionConformanceReport(value)
	if err != nil || !bytes.Equal(encoded, data) {
		return ExtensionConformanceReport{}, errExtensionInvalidReport
	}
	return value, nil
}

func ValidateExtensionConformanceReport(value ExtensionConformanceReport) error {
	descriptor := extension.Descriptor{
		ID: value.ComponentID, Version: value.ComponentVersion, Role: value.Role,
		Operations: extension.OperationsForRole(value.Role), Capabilities: value.Capabilities,
	}
	if value.Schema != ExtensionConformanceReportSchema || value.SchemaVersion != ExtensionConformanceReportSchemaVersion ||
		value.Scope != extensionConformanceScope || value.ContractVersion != extension.ContractVersion ||
		value.ContractSHA256 != extension.ContractSHA256() || value.ProtocolVersion != extension.ProtocolVersion ||
		value.ProtocolSHA256 != extension.ProtocolSHA256() || !validSHA256(value.BundleSHA256) ||
		!validSHA256(value.ManifestSHA256) || !validSHA256(value.ExecutableSHA256) || extension.ValidateDescriptor(descriptor) != nil ||
		(value.CleanupAssurance != "best_effort" && value.CleanupAssurance != "bounded_job") ||
		!value.ProtocolConformant || value.Capabilities == nil || value.Cases == nil ||
		len(value.Cases) == 0 || len(value.Cases) > extension.MaxCollectionEntries {
		return errExtensionInvalidReport
	}
	wantOperations := make([]extension.Operation, 0, len(value.Capabilities))
	for _, operation := range descriptor.Operations {
		if extension.OperationSupported(extension.Manifest{Component: descriptor}, operation) {
			wantOperations = append(wantOperations, operation)
		}
	}
	gotOperations := make([]extension.Operation, 0, len(value.Cases))
	cancellationCases := 0
	previous := ""
	for _, testCase := range value.Cases {
		if !validStandaloneExtensionID(testCase.ID) || testCase.ID <= previous ||
			!extension.OperationSupported(extension.Manifest{Component: descriptor}, testCase.Operation) ||
			(testCase.Terminal != extensionExpectedResult && testCase.Terminal != extensionExpectedError &&
				testCase.Terminal != extensionExpectedCanceled) || testCase.Status != "passed" {
			return errExtensionInvalidReport
		}
		if testCase.Terminal == extensionExpectedCanceled {
			cancellationCases++
		} else {
			gotOperations = append(gotOperations, testCase.Operation)
		}
		previous = testCase.ID
	}
	sort.Slice(gotOperations, func(i, j int) bool { return gotOperations[i] < gotOperations[j] })
	if cancellationCases != 1 || !slices.Equal(gotOperations, wantOperations) {
		return errExtensionInvalidReport
	}
	return nil
}

func decodeCanonicalExtensionDocument(data []byte, maximum int64, target any) error {
	if len(data) < 3 || int64(len(data)) > maximum || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data[:len(data)-1], '\n') >= 0 || bytes.IndexByte(data, '\r') >= 0 ||
		validateJSONNoDuplicateKeys(data[:len(data)-1]) != nil || decodeStrictJSONObject(data[:len(data)-1], target) != nil {
		return errExtensionCompatibility
	}
	return nil
}

func readStableExtensionContractFile(path string, maximum int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > maximum {
		return nil, errExtensionCompatibility
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errExtensionCompatibility
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() != before.Size() || opened.Mode() != before.Mode() {
		_ = file.Close()
		return nil, errExtensionCompatibility
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximum+1))
	after, afterErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || afterErr != nil || closeErr != nil || int64(len(data)) != opened.Size() ||
		!os.SameFile(opened, after) || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) || after.Mode() != opened.Mode() {
		clear(data)
		return nil, errExtensionCompatibility
	}
	return data, nil
}

func validStandaloneExtensionID(value string) bool {
	if value == "" || len(value) > extension.MaxIdentifierBytes || value[0] == '/' || value[len(value)-1] == '/' ||
		bytes.Contains([]byte(value), []byte("//")) {
		return false
	}
	for _, segment := range bytes.Split([]byte(value), []byte{'/'}) {
		if len(segment) == 0 || bytes.Equal(segment, []byte(".")) || bytes.Equal(segment, []byte("..")) {
			return false
		}
		for index, character := range segment {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
				(index > 0 && (character == '-' || character == '_' || character == '.')) {
				continue
			}
			return false
		}
	}
	return true
}

func stringsRepeatHex(character byte) string {
	return string(bytes.Repeat([]byte{character}, extension.SHA256HexCharacters))
}

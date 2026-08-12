package mirror

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/safepath"
)

// CompletePullService is the closed service discriminator for durable complete-pull
// transactions. Its string preserves the JSON wire value; validation is closed.
type CompletePullService string

const (
	CompletePullServiceConfluence CompletePullService = "confluence"
	CompletePullServiceJira       CompletePullService = "jira"
)

// CompletePullArtifactRole is the closed Jira schema-3 artifact vocabulary.
// Legacy Confluence schema-2 intents infer their longstanding artifact meaning
// from canonical paths and therefore keep this field empty on disk.
type CompletePullArtifactRole string

const (
	CompletePullArtifactRoleNative    CompletePullArtifactRole = "native"
	CompletePullArtifactRoleMetadata  CompletePullArtifactRole = "metadata"
	CompletePullArtifactRoleView      CompletePullArtifactRole = "view"
	CompletePullArtifactRoleBase      CompletePullArtifactRole = "base"
	CompletePullArtifactRoleAuxiliary CompletePullArtifactRole = "auxiliary"
)

func validCompletePullService(service CompletePullService) bool {
	return service == CompletePullServiceConfluence || service == CompletePullServiceJira
}

func completePullJournalSchemaFor(service CompletePullService) int {
	if service == CompletePullServiceJira {
		return completePullJiraJournalSchema4
	}
	return completePullConfluenceJournalSchema
}

func completePullPublicationSchemaFor(service CompletePullService) int {
	if service == CompletePullServiceJira {
		return completePullJiraPublicationSchema4
	}
	return completePullConfluencePublicationSchema
}

func completePullJournalSchemaForEntry(service CompletePullService, entry CompletePullJournalEntry) int {
	if service == CompletePullServiceJira && entry.State.Identity == "" && entry.Previous == nil {
		return completePullJiraJournalSchema
	}
	if service == CompletePullServiceConfluence && entry.Includes == nil {
		return completePullJournalSchema
	}
	return completePullJournalSchemaFor(service)
}

func completePullPublicationSchemaForEntry(service CompletePullService, entry CompletePullJournalEntry) int {
	if service == CompletePullServiceJira && entry.State.Identity == "" && entry.Previous == nil {
		return completePullJiraPublicationSchema
	}
	if service == CompletePullServiceConfluence && entry.Includes == nil {
		return completePullPublicationSchema
	}
	return completePullPublicationSchemaFor(service)
}

func validCompletePullJournalSchema(service CompletePullService, schema int) bool {
	if service == CompletePullServiceJira {
		return schema == completePullJiraJournalSchema || schema == completePullJiraJournalSchema4
	}
	return service == CompletePullServiceConfluence && (schema == completePullJournalSchema || schema == completePullConfluenceJournalSchema)
}

func validCompletePullPublicationSchema(service CompletePullService, schema int) bool {
	if service == CompletePullServiceJira {
		return schema == completePullJiraPublicationSchema || schema == completePullJiraPublicationSchema4
	}
	return service == CompletePullServiceConfluence && (schema == completePullPublicationSchema || schema == completePullConfluencePublicationSchema)
}

func validateCompletePullConfluenceEntrySchema(schema int, entry CompletePullJournalEntry) error {
	switch schema {
	case completePullJournalSchema:
		if entry.Includes != nil {
			return fmt.Errorf("%w: legacy Confluence complete-pull schema contains future include evidence", domain.ErrCheckFailed)
		}
	case completePullConfluenceJournalSchema:
		if entry.Includes == nil {
			return fmt.Errorf("%w: current Confluence complete-pull schema omits include evidence", domain.ErrCheckFailed)
		}
		return validateCompletePullIncludeEvidence(*entry.Includes)
	}
	return nil
}

func validateCompletePullJiraEntrySchema(schema int, entry CompletePullJournalEntry) error {
	if schema == completePullJiraJournalSchema || schema == completePullJiraPublicationSchema {
		if entry.State.Identity != "" || entry.Previous != nil {
			return fmt.Errorf("%w: legacy Jira complete-pull schema contains future identity or relocation state", domain.ErrCheckFailed)
		}
		return nil
	}
	if schema == completePullJiraJournalSchema4 || schema == completePullJiraPublicationSchema4 {
		if entry.State.Identity != entry.Identity {
			return fmt.Errorf("%w: Jira complete-pull stable sidecar identity does not match its selected identity", domain.ErrCheckFailed)
		}
		return nil
	}
	return nil
}

func completePullProgressSchemaFor(service CompletePullService) int {
	if service == CompletePullServiceJira {
		return completePullJiraProgressSchema
	}
	return completePullConfluenceProgressSchema
}

func validCompletePullProgressService(checkpoint, progress CompletePullService) bool {
	return checkpoint == CompletePullServiceConfluence && progress == CompletePullServiceConfluence ||
		checkpoint == CompletePullServiceJira && progress == CompletePullServiceJira
}

func staleCompletePullProgressService(checkpoint CompletePullService, progress completePullProgress) bool {
	return checkpoint == CompletePullServiceJira && progress.SchemaVersion == completePullProgressSchema && progress.Service == "" ||
		checkpoint == CompletePullServiceJira && progress.SchemaVersion == completePullConfluenceProgressSchema && progress.Service == CompletePullServiceConfluence ||
		checkpoint == CompletePullServiceConfluence && progress.SchemaVersion == completePullJiraProgressSchema && progress.Service == CompletePullServiceJira
}

func positiveDecimalIdentity(value string) bool {
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func validateCompletePullJournalEntry(service CompletePullService, entry CompletePullJournalEntry) error {
	state := entry.State
	if state.ID == "" || len(state.ID) > maxCompletePullIDBytes {
		return fmt.Errorf("%w: complete-pull journal contains an invalid identity", domain.ErrCheckFailed)
	}
	if !validSHA256(state.Hash) {
		return fmt.Errorf("%w: complete-pull journal state for %q has an invalid version or hash", domain.ErrCheckFailed, state.ID)
	}
	if _, err := NewPublicArtifactPath(state.Path); err != nil {
		return fmt.Errorf("%w: complete-pull journal state for %q has a non-canonical path", domain.ErrCheckFailed, state.ID)
	}
	switch service {
	case CompletePullServiceConfluence:
		if entry.Identity != "" || state.Identity != "" || entry.Previous != nil || !positiveDecimalIdentity(state.ID) || state.Version <= 0 || !strings.HasSuffix(state.Path, ".csf") {
			return fmt.Errorf("%w: complete-pull Confluence journal state is invalid", domain.ErrCheckFailed)
		}
	case CompletePullServiceJira:
		if entry.Includes != nil || !positiveDecimalIdentity(entry.Identity) || state.Version != 0 || safepath.Segment(state.ID) != state.ID || filepath.Base(filepath.FromSlash(state.Path)) != state.ID+".wiki" || (state.Identity != "" && state.Identity != entry.Identity) {
			return fmt.Errorf("%w: complete-pull Jira journal identity, key, version, or native path is invalid", domain.ErrCheckFailed)
		}
		if entry.Previous != nil {
			previous := entry.Previous.State
			if state.Identity != entry.Identity || entry.Previous.View == nil || previous.ID == "" || previous.ID == state.ID || previous.Version != 0 || !validSHA256(previous.Hash) || safepath.Segment(previous.ID) != previous.ID || filepath.Base(filepath.FromSlash(previous.Path)) != previous.ID+".wiki" || (previous.Identity != "" && previous.Identity != entry.Identity) {
				return fmt.Errorf("%w: complete-pull Jira relocation predecessor is invalid", domain.ErrCheckFailed)
			}
		}
	default:
		return fmt.Errorf("%w: complete-pull journal service is invalid", domain.ErrCheckFailed)
	}
	return nil
}

func validateCompletePullArtifactRole(service CompletePullService, entry CompletePullJournalEntry, qualified ArtifactPath, role CompletePullArtifactRole, mode uint32, remove, bestEffort bool) error {
	if service == CompletePullServiceConfluence {
		if role != "" {
			return fmt.Errorf("legacy Confluence publication cannot persist an artifact role")
		}
		if qualified.class == artifactPathClassPrivateBase {
			base := confluenceCompletePullBasePath(entry)
			if qualified.String() != base || mode != 0o600 || remove || bestEffort {
				return fmt.Errorf("confluence pristine-base artifact does not match the accepted page identity")
			}
		}
		return nil
	}
	if service != CompletePullServiceJira {
		return fmt.Errorf("unknown publication service")
	}
	state := entry.State
	stem := strings.TrimSuffix(state.Path, ".wiki")
	rel := qualified.String()
	if remove && entry.Previous != nil {
		previous := entry.Previous.State
		previousStem := strings.TrimSuffix(previous.Path, ".wiki")
		validRemoval := mode == 0 && !bestEffort
		switch role {
		case CompletePullArtifactRoleNative:
			validRemoval = validRemoval && rel == previous.Path && qualified.class == artifactPathClassPublic
		case CompletePullArtifactRoleMetadata:
			validRemoval = validRemoval && rel == previousStem+".json" && qualified.class == artifactPathClassPublic
		case CompletePullArtifactRoleView:
			validRemoval = validRemoval && rel == previousStem+".md" && qualified.class == artifactPathClassPublic
		case CompletePullArtifactRoleBase:
			validRemoval = validRemoval && rel == filepath.ToSlash(filepath.Join(".atl", "base", previous.ID+".wiki")) && qualified.class == artifactPathClassPrivateBase
		case CompletePullArtifactRoleAuxiliary:
			validRemoval = validRemoval && qualified.class == artifactPathClassPublic &&
				(rel == previousStem+".epic-children.json" || rel == previousStem+".comments.json" ||
					rel == previousStem+".attachments.json" || strings.HasPrefix(rel, previousStem+".assets/") ||
					strings.HasPrefix(rel, previousStem+".attachments/"))
		default:
			validRemoval = false
		}
		if !validRemoval {
			return fmt.Errorf("invalid Jira relocation retirement artifact")
		}
		return nil
	}
	writable := func(path string, class artifactPathClass, wantMode uint32, wantBestEffort bool) bool {
		return rel == path && qualified.class == class && !remove && mode == wantMode && bestEffort == wantBestEffort
	}
	switch role {
	case CompletePullArtifactRoleNative:
		if !writable(state.Path, artifactPathClassPublic, 0o644, false) {
			return fmt.Errorf("invalid Jira native artifact")
		}
	case CompletePullArtifactRoleMetadata:
		if !writable(stem+".json", artifactPathClassPublic, 0o644, false) {
			return fmt.Errorf("invalid Jira metadata artifact")
		}
	case CompletePullArtifactRoleView:
		if !writable(stem+".md", artifactPathClassPublic, 0o644, true) {
			return fmt.Errorf("invalid Jira view artifact")
		}
	case CompletePullArtifactRoleBase:
		base := filepath.ToSlash(filepath.Join(".atl", "base", state.ID+".wiki"))
		if !writable(base, artifactPathClassPrivateBase, 0o600, false) {
			return fmt.Errorf("invalid Jira pristine-base artifact")
		}
	case CompletePullArtifactRoleAuxiliary:
		epic := stem + ".epic-children.json"
		comments := stem + ".comments.json"
		attachments := stem + ".attachments.json"
		assetPrefix := stem + ".assets/"
		attachmentPrefix := stem + ".attachments/"
		switch {
		case rel == epic && qualified.class == artifactPathClassPublic && !bestEffort:
			if remove {
				if mode != 0 {
					return fmt.Errorf("invalid Jira auxiliary removal")
				}
			} else if mode != 0o644 {
				return fmt.Errorf("invalid Jira auxiliary mode")
			}
		case strings.HasPrefix(rel, assetPrefix) && qualified.class == artifactPathClassPublic && !remove && !bestEffort && mode == 0o644:
		case (rel == comments || rel == attachments) && writable(rel, artifactPathClassPublic, 0o600, false):
		case strings.HasPrefix(rel, attachmentPrefix) && qualified.class == artifactPathClassPublic && !remove && !bestEffort && mode == 0o600:
		default:
			return fmt.Errorf("invalid Jira auxiliary artifact")
		}
	default:
		return fmt.Errorf("unknown Jira artifact role")
	}
	return nil
}

func validateJiraArtifactRoleCounts(roles []CompletePullArtifactRole) error {
	counts := map[CompletePullArtifactRole]int{}
	for _, role := range roles {
		counts[role]++
	}
	for _, required := range []CompletePullArtifactRole{
		CompletePullArtifactRoleNative,
		CompletePullArtifactRoleMetadata,
		CompletePullArtifactRoleView,
		CompletePullArtifactRoleBase,
	} {
		if counts[required] != 1 {
			return fmt.Errorf("jira publication requires exactly one %s artifact", required)
		}
	}
	return nil
}

func validateCompletePullPublication(intent completePullPublicationIntent, checkpoint CompletePullCheckpoint, stageDir string) error {
	if !validCompletePullService(intent.Service) || !validCompletePullPublicationSchema(intent.Service, intent.SchemaVersion) || intent.Service != checkpoint.Service || intent.SelectorSHA256 != checkpoint.SelectorSHA256 || intent.OptionsSHA256 != checkpoint.OptionsSHA256 || intent.SelectionSHA256 != checkpoint.SelectionSHA256 {
		return fmt.Errorf("%w: complete-pull publication binding is invalid", domain.ErrCheckFailed)
	}
	if intent.Service == CompletePullServiceJira {
		if err := validateCompletePullJiraEntrySchema(intent.SchemaVersion, intent.Entry); err != nil {
			return err
		}
	} else if err := validateCompletePullConfluenceEntrySchema(intent.SchemaVersion, intent.Entry); err != nil {
		return err
	}
	if intent.Index < checkpoint.NextIndex || intent.Index >= len(checkpoint.IDs) {
		return fmt.Errorf("%w: complete-pull publication index is outside the pending selection", domain.ErrCheckFailed)
	}
	selectedIdentity := intent.Entry.State.ID
	if intent.Service == CompletePullServiceJira {
		selectedIdentity = intent.Entry.Identity
	}
	if checkpoint.IDs[intent.Index] != selectedIdentity {
		return fmt.Errorf("%w: complete-pull publication identity is outside the pending selection", domain.ErrCheckFailed)
	}
	if !validCompletePullWriteToken(intent.WriteToken) {
		return fmt.Errorf("%w: complete-pull publication write token is invalid", domain.ErrCheckFailed)
	}
	if err := validateCompletePullJournalEntry(intent.Service, intent.Entry); err != nil {
		return err
	}
	count := len(intent.Artifacts)
	if intent.Relocation != nil {
		if (intent.Service == CompletePullServiceConfluence && intent.Entry.Previous != nil) ||
			(intent.Service == CompletePullServiceJira && intent.Entry.Previous == nil) {
			return fmt.Errorf("%w: complete-pull relocation does not match its service-specific predecessor", domain.ErrCheckFailed)
		}
		count += len(intent.Relocation.Artifacts)
		if intent.Relocation.Next < 0 || intent.Relocation.Next > len(intent.Relocation.Artifacts) {
			return fmt.Errorf("%w: complete-pull relocation progress is invalid", domain.ErrCheckFailed)
		}
	}
	if count == 0 || count > maxCompletePullPublicationArtifacts || intent.Next < 0 || intent.Next > len(intent.Artifacts) {
		return fmt.Errorf("%w: complete-pull publication artifact bounds are invalid", domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, count)
	roles := make([]CompletePullArtifactRole, 0, len(intent.Artifacts))
	var total int64
	tempNameBytes := len(completePullJournalTemp(intent.WriteToken)) + len(completePullSidecarTemp(intent.WriteToken)) + len(completePullProgressTemp(intent.WriteToken))
	writeIndex := 0
	validate := func(artifact completePullPublicationArtifact) error {
		if err := validatePublicationArtifact(intent.Service, intent.Entry, artifact, stageDir, true, intent.WriteToken, writeIndex); err != nil {
			return err
		}
		if !artifact.Remove {
			tempNameBytes += len(artifact.Temp)
			writeIndex++
		}
		key := registrationPathKey(artifact.Path)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate publication destination")
		}
		seen[key] = struct{}{}
		total += artifact.Size
		return nil
	}
	for _, artifact := range intent.Artifacts {
		if err := validate(artifact); err != nil {
			return fmt.Errorf("%w: invalid complete-pull publication artifact: %v", domain.ErrCheckFailed, err)
		}
		roles = append(roles, artifact.Role)
	}
	switch intent.Service {
	case CompletePullServiceConfluence:
		if err := validateConfluenceCompletePullIntent(intent.Entry, intent.Artifacts); err != nil {
			return fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
		}
	case CompletePullServiceJira:
		if err := validateJiraArtifactRoleCounts(roles); err != nil {
			return fmt.Errorf("%w: %v", domain.ErrCheckFailed, err)
		}
	}
	if intent.Relocation != nil {
		for _, artifact := range intent.Relocation.Artifacts {
			if err := validate(artifact); err != nil {
				return fmt.Errorf("%w: invalid complete-pull relocation artifact: %v", domain.ErrCheckFailed, err)
			}
		}
	}
	if total > maxCompletePullPublicationBytes {
		return fmt.Errorf("%w: complete-pull publication exceeds %d staged bytes", domain.ErrCheckFailed, maxCompletePullPublicationBytes)
	}
	tempNameCount := writeIndex + 3
	if tempNameCount > maxCompletePullPublicationArtifacts+3 || tempNameBytes > (maxCompletePullPublicationArtifacts+3)*maxCompletePullTempName {
		return fmt.Errorf("%w: complete-pull publication temporary-file declarations exceed their bound", domain.ErrCheckFailed)
	}
	if intent.Committed && (intent.Next != len(intent.Artifacts) || (intent.Relocation != nil && intent.Relocation.Next != len(intent.Relocation.Artifacts))) {
		return fmt.Errorf("%w: committed complete-pull publication has unfinished artifacts", domain.ErrCheckFailed)
	}
	return nil
}

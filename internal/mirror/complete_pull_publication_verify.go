package mirror

import (
	"fmt"
	"reflect"

	"github.com/isukharev/atl/internal/domain"
)

func (m *Mirror) verifyCommittedPublication(checkpoint CompletePullCheckpoint, intent completePullPublicationIntent) error {
	for _, artifact := range intent.Artifacts {
		current, err := publicationCurrentForIntentPostcondition(m.Root, artifact)
		if err != nil || !publicationMatchesPost(current, artifact) {
			return fmt.Errorf("%w: committed complete-pull artifact %s does not match its exact postcondition", domain.ErrCheckFailed, artifact.Path)
		}
	}
	if intent.Relocation != nil {
		state, ok, err := m.SyncStateOf(intent.Entry.State.ID)
		if err != nil || !ok || state != intent.Entry.State {
			return fmt.Errorf("%w: committed complete-pull relocation has no exact canonical state", domain.ErrCheckFailed)
		}
		view, ok, err := m.ViewStateOf(intent.Entry.State.ID)
		if err != nil || !ok || !reflect.DeepEqual(view, intent.Entry.View) {
			return fmt.Errorf("%w: committed complete-pull relocation has no exact view state", domain.ErrCheckFailed)
		}
		if intent.Entry.Previous != nil {
			if _, oldFound, oldErr := m.SyncStateOf(intent.Entry.Previous.State.ID); oldErr != nil || oldFound {
				return fmt.Errorf("%w: committed Jira relocation retained its old canonical sidecar key", domain.ErrCheckFailed)
			}
		}
		for _, artifact := range intent.Relocation.Artifacts {
			current, err := publicationCurrentForIntentPostcondition(m.Root, artifact)
			if err != nil || !publicationMatchesPost(current, artifact) {
				return fmt.Errorf("%w: committed complete-pull relocation artifact %s does not match its exact postcondition", domain.ErrCheckFailed, artifact.Path)
			}
		}
	}
	if intent.Eligible {
		journal, found, err := m.loadCompletePullJournal(checkpoint.SelectorSHA256)
		if err != nil || !found {
			return fmt.Errorf("%w: committed complete-pull publication has no accepted journal evidence", domain.ErrCheckFailed)
		}
		if err := validateCompletePullJournal(journal, checkpoint); err != nil {
			return err
		}
		position := intent.Index - journal.StartIndex
		if position < 0 || position >= len(journal.Entries) || !reflect.DeepEqual(journal.Entries[position], intent.Entry) {
			return fmt.Errorf("%w: committed complete-pull publication differs from accepted journal evidence", domain.ErrCheckFailed)
		}
		switch intent.Service {
		case CompletePullServiceConfluence:
			if _, verifyErr := m.verifyConfluenceCompletePullAttachmentArtifacts(intent.Entry, true); verifyErr != nil {
				return fmt.Errorf("%w: committed complete-pull attachment artifacts do not match their accepted evidence", domain.ErrCheckFailed)
			}
		case CompletePullServiceJira:
			if verifyErr := m.verifyJiraCompletePullOptionalArtifacts(intent.Entry); verifyErr != nil {
				return fmt.Errorf("%w: committed complete-pull Jira optional artifacts do not match their accepted evidence", domain.ErrCheckFailed)
			}
		}
	} else {
		state, ok, err := m.SyncStateOf(intent.Entry.State.ID)
		if err != nil || !ok || state != intent.Entry.State {
			return fmt.Errorf("%w: committed ineligible complete-pull publication has no exact canonical state", domain.ErrCheckFailed)
		}
	}
	return nil
}

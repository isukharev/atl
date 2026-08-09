package agenteval

import (
	"fmt"
	"slices"
)

func standaloneOperationKey(id, mode string) string {
	return id + "\x00" + mode
}

func standaloneVersionedContractKey(namespace, kind string, version int) string {
	return fmt.Sprintf("%s\x00%020d", standaloneContractKey(namespace, kind), version)
}

func standaloneTransitionKey(from, to string) string {
	return from + "\x00" + to
}

func standaloneStringSliceMapEqual(left, right map[string][]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValues := range left {
		if !slices.Equal(leftValues, right[key]) {
			return false
		}
	}
	return true
}

func standaloneProofSetsEqual(left, right [][]string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !slices.Equal(left[index], right[index]) {
			return false
		}
	}
	return true
}

func standaloneExpectedAttemptTransitions() map[string][][]string {
	return map[string][][]string{
		standaloneTransitionKey("committed", "canceled"):    {{"durable_cancel", "non_execution_proof"}},
		standaloneTransitionKey("committed", "failed"):      {{"definitive_spawn_failure", "non_execution_proof"}},
		standaloneTransitionKey("committed", "spawning"):    {{"durable_spawn_intent"}},
		standaloneTransitionKey("committed", "timed_out"):   {{"durable_deadline", "non_execution_proof"}},
		standaloneTransitionKey("committed", "unknown"):     {{"incomplete_terminal_evidence"}},
		standaloneTransitionKey("planned", "canceled"):      {{"complete_ledger", "durable_cancel", "no_commit"}},
		standaloneTransitionKey("planned", "committed"):     {{"durable_commit"}},
		standaloneTransitionKey("planned", "policy_denied"): {{"complete_ledger", "durable_policy_refusal", "no_commit"}},
		standaloneTransitionKey("planned", "timed_out"):     {{"complete_ledger", "durable_deadline", "no_commit"}},
		standaloneTransitionKey("planned", "unknown"):       {{"incomplete_terminal_evidence"}},
		standaloneTransitionKey("planned", "unsupported"):   {{"complete_ledger", "durable_capability_refusal", "no_commit"}},
		standaloneTransitionKey("running", "canceled"):      {{"durable_cancel", "termination_proof"}},
		standaloneTransitionKey("running", "failed"):        {{"terminal_receipt", "termination_proof"}},
		standaloneTransitionKey("running", "succeeded"):     {{"terminal_receipt", "termination_proof"}},
		standaloneTransitionKey("running", "timed_out"):     {{"durable_deadline", "termination_proof"}},
		standaloneTransitionKey("running", "unknown"):       {{"incomplete_terminal_evidence"}},
		standaloneTransitionKey("spawning", "canceled"):     {{"durable_cancel", "non_execution_proof"}, {"durable_cancel", "termination_proof"}},
		standaloneTransitionKey("spawning", "failed"):       {{"definitive_spawn_failure", "non_execution_proof"}, {"terminal_receipt", "termination_proof"}},
		standaloneTransitionKey("spawning", "running"):      {{"durable_process_identity"}},
		standaloneTransitionKey("spawning", "succeeded"):    {{"terminal_receipt", "termination_proof"}},
		standaloneTransitionKey("spawning", "timed_out"):    {{"durable_deadline", "non_execution_proof"}, {"durable_deadline", "termination_proof"}},
		standaloneTransitionKey("spawning", "unknown"):      {{"incomplete_terminal_evidence"}},
	}
}

func standaloneValidSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func standaloneOneOf(value string, allowed ...string) bool {
	return slices.Contains(allowed, value)
}

func standaloneInt(value int) *int {
	return &value
}

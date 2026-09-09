package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/isukharev/atl/internal/diagnostic"
	"github.com/isukharev/atl/internal/domain"
)

func TestDirectJiraCommentRejectsOperationTicketWithoutBackendRequest(t *testing.T) {
	server := newJiraCommentCLIServer(t)
	out, _, err := executeCLIRaw(t, jiraEnv(server.srv),
		"jira", "issue", "comment", "add", "PROJ-1",
		"--from-file", writeCommentBody(t, "body"), "--apply",
		"--expected-proposal-hash", strings.Repeat("a", 64), "--operation-ticket", "ticket-1",
	)
	if out != "" || !errors.Is(err, domain.ErrUsage) || !strings.Contains(err.Error(), "only in Broker mode") {
		t.Fatalf("stdout=%q err=%v", out, err)
	}
	if server.myself != 0 || server.issue != 0 || server.list != 0 || server.post != 0 {
		t.Fatalf("requests myself=%d issue=%d list=%d post=%d", server.myself, server.issue, server.list, server.post)
	}
}

func TestBrokerJiraCommentApplyRequiresOperationTicketBeforeSession(t *testing.T) {
	directory := t.TempDir()
	configBody := `{"connection_mode":"broker","broker":{"base_url":"https://broker.example.test","broker_id":"broker-1","audience":"atl-broker","jira_session_file":"/definitely/missing/writer.json"},"jira_list_views":{}}`
	if err := os.WriteFile(filepath.Join(directory, "config.json"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := executeCLIRaw(t, map[string]string{"ATL_CONFIG_DIR": directory, "ATL_NO_UPDATE": "1"},
		"jira", "issue", "comment", "add", "PROJ-1",
		"--from-file", writeCommentBody(t, "body"), "--apply", "--expected-proposal-hash", strings.Repeat("a", 64),
	)
	if out != "" || !errors.Is(err, domain.ErrUsage) || !strings.Contains(err.Error(), "--operation-ticket is required") {
		t.Fatalf("stdout=%q err=%v", out, err)
	}
}

func TestJiraCommentOperationTicketRequiresApplyBeforeConfiguration(t *testing.T) {
	out, _, err := executeCLIRaw(t, map[string]string{"ATL_JIRA_URL": "not a URL"},
		"jira", "issue", "comment", "add", "PROJ-1", "--from-file", "missing", "--operation-ticket", "ticket-1",
	)
	if out != "" || !errors.Is(err, domain.ErrUsage) || !strings.Contains(err.Error(), "requires --apply") {
		t.Fatalf("stdout=%q err=%v", out, err)
	}
}

func TestJiraCommentOutcomeRequiresBrokerAndIndependentObserver(t *testing.T) {
	directOut, _, directErr := executeCLIRaw(t, map[string]string{"ATL_NO_UPDATE": "1"},
		"jira", "issue", "comment", "outcome", "--operation-ticket", "ticket-1",
	)
	if directOut != "" || !errors.Is(directErr, domain.ErrUsage) || !strings.Contains(directErr.Error(), "requires Broker mode") {
		t.Fatalf("direct stdout=%q err=%v", directOut, directErr)
	}

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	writerOnly := `{"connection_mode":"broker","broker":{"base_url":"https://broker.example.test","broker_id":"broker-1","audience":"atl-broker","jira_session_file":"/definitely/missing/writer.json"},"jira_list_views":{}}`
	if err := os.WriteFile(configPath, []byte(writerOnly), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err := executeCLIRaw(t, map[string]string{"ATL_CONFIG_DIR": directory, "ATL_NO_UPDATE": "1"},
		"jira", "issue", "comment", "outcome", "--operation-ticket", "ticket-1",
	)
	if out != "" || !errors.Is(err, domain.ErrConfig) || !strings.Contains(err.Error(), "jira_observation_session_file") {
		t.Fatalf("writer-only stdout=%q err=%v", out, err)
	}

	separate := `{"connection_mode":"broker","broker":{"base_url":"https://broker.example.test","broker_id":"broker-1","audience":"atl-broker","jira_observation_session_file":"/definitely/missing/observer.json"},"jira_list_views":{}}`
	if err := os.WriteFile(configPath, []byte(separate), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, err = executeCLIRaw(t, map[string]string{"ATL_CONFIG_DIR": directory, "ATL_NO_UPDATE": "1", "ATL_READ_ONLY": "1"},
		"jira", "issue", "comment", "outcome", "--operation-ticket", "ticket-1",
	)
	if out != "" || !errors.Is(err, domain.ErrUsage) {
		t.Fatalf("observer stdout=%q err=%v", out, err)
	}
}

func TestBrokerCommentOutputFailureIsSameTicketAmbiguity(t *testing.T) {
	cause := errors.New("stdout unavailable")
	err := brokerCommentResultErr(nil, cause, true)
	var ambiguity interface{ DiagnosticAmbiguousWrite() bool }
	if !errors.Is(err, domain.ErrCheckFailed) || !errors.Is(err, cause) || !errors.As(err, &ambiguity) || !ambiguity.DiagnosticAmbiguousWrite() ||
		!strings.Contains(err.Error(), "<SAME-TICKET>") {
		t.Fatalf("err=%v", err)
	}
	recovery := diagnostic.Recover(err, diagnostic.OperationWrite)
	if recovery.Action != diagnostic.RecoveryReconcileWriteOutcome || recovery.RetrySafe {
		t.Fatalf("recovery=%+v", recovery)
	}
}

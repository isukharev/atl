package agenteval

import "encoding/json"

type JiraIssueGraphSourceFailure struct {
	Class      string `json:"class"`
	HTTPStatus *int   `json:"http_status,omitempty"`
}

func validateJiraGraphSourceMembers(source map[string]json.RawMessage, owner string) error {
	if err := jiraGraphWireMembers(source, owner,
		[]string{"node_id", "node_depth", "kind", "requested", "status", "complete", "count", "truncated", "stability"},
		[]string{"partial_reason", "failure"}); err != nil {
		return err
	}
	if raw, ok := source["failure"]; ok {
		failure, err := jiraGraphWireObject(raw, owner+".failure")
		if err != nil {
			return err
		}
		return jiraGraphWireMembers(failure, owner+".failure", []string{"class"}, []string{"http_status"})
	}
	return nil
}

func validJiraGraphWireRemoteLinkFailure(source JiraIssueGraphSource) bool {
	failure := source.Failure
	if failure == nil {
		return true
	}
	if source.Kind != "remote_links" || !source.Requested || source.Complete || source.Count != 0 || source.Truncated {
		return false
	}
	switch failure.Class {
	case "authentication":
		return source.Status == "forbidden" && source.PartialReason == "" &&
			(failure.HTTPStatus == nil || *failure.HTTPStatus == 401)
	case "permission":
		return source.Status == "forbidden" && source.PartialReason == "" &&
			(failure.HTTPStatus == nil || *failure.HTTPStatus == 403)
	case "not_found":
		return source.Status == "unsupported" && source.PartialReason == "" &&
			(failure.HTTPStatus == nil || *failure.HTTPStatus == 404)
	case "http":
		return failure.HTTPStatus != nil && *failure.HTTPStatus >= 300 && *failure.HTTPStatus <= 599 &&
			*failure.HTTPStatus != 401 && *failure.HTTPStatus != 403 && *failure.HTTPStatus != 404 &&
			source.Status == "partial" && (source.PartialReason == "request_failed" || source.PartialReason == "malformed_response")
	case "transport", "request":
		return failure.HTTPStatus == nil && source.Status == "partial" && source.PartialReason == "request_failed"
	default:
		return false
	}
}

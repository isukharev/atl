package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/isukharev/atl/internal/brokercontract"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

var _ domain.BrokerJiraAttachmentPortV3 = (*Jira)(nil)

type brokerJiraAttachmentIssueWire struct {
	ID     string                         `json:"id"`
	Key    string                         `json:"key"`
	Fields brokerJiraAttachmentFieldsWire `json:"fields"`
	Self   string                         `json:"self,omitempty"`
	Expand string                         `json:"expand,omitempty"`
}

type brokerJiraAttachmentFieldsWire struct {
	Attachment []json.RawMessage `json:"attachment"`
	Project    json.RawMessage   `json:"project"`
	Updated    json.RawMessage   `json:"updated"`
}

type brokerJiraAttachmentItemWire struct {
	ID        string          `json:"id"`
	Filename  string          `json:"filename"`
	MediaType string          `json:"mimeType"`
	Size      int64           `json:"size"`
	Created   string          `json:"created"`
	Content   string          `json:"content"`
	Author    json.RawMessage `json:"author,omitempty"`
	Self      string          `json:"self,omitempty"`
	Thumbnail string          `json:"thumbnail,omitempty"`
}

func (j *Jira) QualifyBrokerJiraAttachment(ctx context.Context, issueKey, attachmentID string) (domain.BrokerJiraAttachmentSnapshotV3, error) {
	snapshot, _, err := j.readBrokerJiraAttachment(ctx, issueKey, attachmentID)
	return snapshot, err
}

func (j *Jira) PrepareBrokerJiraAttachment(ctx context.Context, expected domain.BrokerJiraAttachmentSnapshotV3) (domain.BrokerJiraAttachmentOpenHandleV3, error) {
	if _, err := brokercontract.AttachmentSnapshotSHA256V3(expected); err != nil {
		return nil, brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	current, contentURI, err := j.readBrokerJiraAttachment(ctx, expected.IssueKey, expected.AttachmentID)
	if err != nil {
		return nil, err
	}
	if current != expected {
		return nil, brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	return newBrokerJiraAttachmentHandle(j, current, contentURI), nil
}

func (j *Jira) readBrokerJiraAttachment(ctx context.Context, issueKey, attachmentID string) (domain.BrokerJiraAttachmentSnapshotV3, string, error) {
	if j == nil || j.c == nil || !validBrokerJiraAttachmentArguments(issueKey, attachmentID) {
		return domain.BrokerJiraAttachmentSnapshotV3{}, "", brokerJiraAttachmentFailure(domain.ErrUsage)
	}
	requestCtx, err := brokerJiraAttachmentBudgetContext(ctx, brokercontract.MaxAttachmentMetadataResponseBytesV3)
	if err != nil {
		return domain.BrokerJiraAttachmentSnapshotV3{}, "", err
	}
	const fields = "attachment%2Cproject%2Cupdated"
	path := "/rest/api/2/issue/" + url.PathEscape(issueKey) + "?fields=" + fields
	data, err := j.c.DoWithBodyLimit(requestCtx, http.MethodGet, path, nil, nil, brokercontract.MaxAttachmentMetadataResponseBytesV3)
	if err != nil {
		return domain.BrokerJiraAttachmentSnapshotV3{}, "", brokerJiraAttachmentFailure(err)
	}
	return j.decodeBrokerJiraAttachment(data, issueKey, attachmentID)
}

func (j *Jira) decodeBrokerJiraAttachment(data []byte, issueKey, attachmentID string) (domain.BrokerJiraAttachmentSnapshotV3, string, error) {
	var response brokerJiraAttachmentIssueWire
	if strictjson.DecodeExact(data, brokercontract.MaxCanonicalDepth, &response) != nil || response.invalid() ||
		response.Key != issueKey || !validBrokerJiraAttachmentDecimal(response.ID) || !domain.ValidJiraIssueKey(response.Key) || len(response.Key) > 64 ||
		response.Self != "" && !validBrokerJiraAttachmentSupportingText(response.Self) || response.Expand != "" && !validBrokerJiraAttachmentSupportingText(response.Expand) {
		return domain.BrokerJiraAttachmentSnapshotV3{}, "", brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	var project map[string]json.RawMessage
	var projectKey, updated string
	if strictjson.Decode(response.Fields.Project, &project) != nil || project == nil || !brokerJiraProjectMetadata(project) ||
		strictjson.Decode(project["key"], &projectKey) != nil || !strings.HasPrefix(response.Key, projectKey+"-") ||
		strictjson.Decode(response.Fields.Updated, &updated) != nil || len(updated) == 0 || len(updated) > 4<<10 || !validBrokerJiraAttachmentText(updated) ||
		len(response.Fields.Attachment) > brokercontract.MaxAttachmentInventoryItemsV3 {
		return domain.BrokerJiraAttachmentSnapshotV3{}, "", brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	seen := make(map[string]struct{}, len(response.Fields.Attachment))
	var selected domain.BrokerJiraAttachmentSnapshotV3
	var selectedURI string
	found := false
	for _, raw := range response.Fields.Attachment {
		var item brokerJiraAttachmentItemWire
		if strictjson.DecodeExact(raw, brokercontract.MaxCanonicalDepth, &item) != nil || !validBrokerJiraAttachmentItem(item) {
			return domain.BrokerJiraAttachmentSnapshotV3{}, "", brokerJiraAttachmentFailure(domain.ErrCheckFailed)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return domain.BrokerJiraAttachmentSnapshotV3{}, "", brokerJiraAttachmentFailure(domain.ErrCheckFailed)
		}
		seen[item.ID] = struct{}{}
		if !validBrokerJiraAttachmentContentURI(j.base, item.Content, item.ID, item.Filename) {
			return domain.BrokerJiraAttachmentSnapshotV3{}, "", brokerJiraAttachmentFailure(domain.ErrCheckFailed)
		}
		if item.ID != attachmentID {
			continue
		}
		found = true
		selected = domain.BrokerJiraAttachmentSnapshotV3{
			IssueID: response.ID, IssueKey: response.Key, Project: projectKey, Updated: updated,
			AttachmentID: item.ID, ParentID: response.ID, Filename: item.Filename, MediaType: item.MediaType,
			Created: item.Created, DeclaredSize: item.Size,
		}
		selectedURI = item.Content
	}
	if !found {
		return domain.BrokerJiraAttachmentSnapshotV3{}, "", brokerJiraAttachmentFailure(domain.ErrNotFound)
	}
	selected, err := brokercontract.NewAttachmentSnapshotEvidenceV3(selected)
	if err != nil {
		return domain.BrokerJiraAttachmentSnapshotV3{}, "", brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	return selected, selectedURI, nil
}

func (value brokerJiraAttachmentIssueWire) invalid() bool {
	return value.Fields.Attachment == nil || len(value.Fields.Project) == 0 || len(value.Fields.Updated) == 0
}

func validBrokerJiraAttachmentArguments(issueKey, attachmentID string) bool {
	return len(issueKey) <= 64 && domain.ValidJiraIssueKey(issueKey) && validBrokerJiraAttachmentDecimal(attachmentID)
}

func validBrokerJiraAttachmentDecimal(value string) bool {
	if value == "" || len(value) > 64 || value[0] == '0' {
		return false
	}
	for _, current := range value {
		if current < '0' || current > '9' {
			return false
		}
	}
	return true
}

func validBrokerJiraAttachmentItem(value brokerJiraAttachmentItemWire) bool {
	if !validBrokerJiraAttachmentDecimal(value.ID) || len(value.Filename) == 0 || len(value.Filename) > 255 || !utf8.ValidString(value.Filename) ||
		strings.TrimSpace(value.Filename) == "" || value.Filename == "." || value.Filename == ".." || strings.ContainsAny(value.Filename, "/\\%") ||
		len(value.MediaType) == 0 || len(value.MediaType) > 255 || value.Size < 0 || value.Size > brokercontract.MaxAttachmentNativeBodyBytesV3 ||
		len(value.Created) == 0 || len(value.Created) > 4<<10 || !validBrokerJiraAttachmentText(value.Created) || len(value.Content) == 0 || int64(len(value.Content)) > brokercontract.MaxAttachmentContentURIBytesV3 || !utf8.ValidString(value.Content) {
		return false
	}
	for _, current := range value.Filename {
		if current < 0x20 || current == 0x7f {
			return false
		}
	}
	for _, current := range value.MediaType {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	if value.Author != nil && !brokerJiraBoundedSupportingValue(value.Author) {
		return false
	}
	return (value.Self == "" || validBrokerJiraAttachmentSupportingText(value.Self)) && (value.Thumbnail == "" || validBrokerJiraAttachmentSupportingText(value.Thumbnail))
}

func validBrokerJiraAttachmentSupportingText(value string) bool {
	return len(value) > 0 && len(value) <= 64<<10 && validBrokerJiraAttachmentText(value)
}

func validBrokerJiraAttachmentText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if current < 0x20 && current != '\t' && current != '\n' && current != '\r' {
			return false
		}
	}
	return true
}

func validBrokerJiraAttachmentContentURI(base, raw, attachmentID, filename string) bool {
	if raw == "" || int64(len(raw)) > brokercontract.MaxAttachmentContentURIBytesV3 || !utf8.ValidString(raw) || strings.Contains(filename, "%") {
		return false
	}
	for _, current := range []byte(raw) {
		if current < 0x21 || current > 0x7e {
			return false
		}
	}
	baseURL, err := url.Parse(base)
	if err != nil || !baseURL.IsAbs() || baseURL.Opaque != "" || baseURL.User != nil || baseURL.Host == "" || baseURL.RawQuery != "" || baseURL.ForceQuery || baseURL.Fragment != "" || baseURL.RawFragment != "" {
		return false
	}
	content, err := url.Parse(raw)
	if err != nil || content.Opaque != "" || content.User != nil || content.RawQuery != "" || content.ForceQuery || content.Fragment != "" || content.RawFragment != "" {
		return false
	}
	relative := "/secure/attachment/" + attachmentID + "/" + url.PathEscape(filename)
	if content.IsAbs() {
		if !strings.EqualFold(content.Scheme, baseURL.Scheme) || !strings.EqualFold(content.Host, baseURL.Host) {
			return false
		}
		return raw == baseURL.Scheme+"://"+baseURL.Host+strings.TrimSuffix(baseURL.EscapedPath(), "/")+relative
	}
	return content.Scheme == "" && content.Host == "" && strings.HasPrefix(raw, "/") && raw == relative
}

func brokerJiraAttachmentBudgetContext(ctx context.Context, maximum int64) (context.Context, error) {
	if ctx == nil || maximum <= 0 {
		return nil, brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	parent := domain.ReadBudgetFromContext(ctx)
	if parent == nil {
		return nil, brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	budget, err := domain.NewChildReadBudget(parent, 1, maximum)
	if err != nil {
		return nil, brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	return domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(domain.WithReadBudget(ctx, budget))), nil
}

type brokerJiraAttachmentHandleState uint8

const (
	brokerJiraAttachmentPrepared brokerJiraAttachmentHandleState = iota + 1
	brokerJiraAttachmentOpening
	brokerJiraAttachmentOpen
	brokerJiraAttachmentClosing
	brokerJiraAttachmentClosed
)

type brokerJiraAttachmentHandle struct {
	mu        sync.Mutex
	jira      *Jira
	snapshot  domain.BrokerJiraAttachmentSnapshotV3
	uri       string
	state     brokerJiraAttachmentHandleState
	cancel    context.CancelFunc
	body      io.ReadCloser
	openDone  chan struct{}
	closeDone chan struct{}
	closeErr  error
}

func newBrokerJiraAttachmentHandle(j *Jira, snapshot domain.BrokerJiraAttachmentSnapshotV3, uri string) *brokerJiraAttachmentHandle {
	return &brokerJiraAttachmentHandle{jira: j, snapshot: snapshot, uri: uri, state: brokerJiraAttachmentPrepared, closeDone: make(chan struct{})}
}

func (h *brokerJiraAttachmentHandle) Snapshot() domain.BrokerJiraAttachmentSnapshotV3 {
	if h == nil {
		return domain.BrokerJiraAttachmentSnapshotV3{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshot
}

func (h *brokerJiraAttachmentHandle) Open(ctx context.Context, dispatchNotAfter time.Time) (io.ReadCloser, error) {
	if h == nil {
		return nil, brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	h.mu.Lock()
	if h.state != brokerJiraAttachmentPrepared {
		h.mu.Unlock()
		return nil, brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	bodyCtx := ctx
	cancel := context.CancelFunc(func() {})
	if ctx != nil {
		bodyCtx, cancel = context.WithCancel(ctx)
	}
	uri := h.uri
	h.uri = ""
	h.state = brokerJiraAttachmentOpening
	h.cancel = cancel
	h.openDone = make(chan struct{})
	openDone := h.openDone
	h.mu.Unlock()

	requestCtx, err := brokerJiraAttachmentBudgetContext(bodyCtx, brokercontract.MaxAttachmentNativeBodyBytesV3)
	var body io.ReadCloser
	if err == nil {
		body, err = h.jira.c.GetStreamBefore(requestCtx, uri, dispatchNotAfter)
	}

	h.mu.Lock()
	if h.state == brokerJiraAttachmentClosing {
		h.mu.Unlock()
		if body != nil {
			_ = body.Close()
		}
		cancel()
		close(openDone)
		return nil, brokerJiraAttachmentFailure(context.Canceled)
	}
	if err != nil {
		h.cancel = nil
		h.state = brokerJiraAttachmentClosed
		h.closeErr = nil
		close(openDone)
		close(h.closeDone)
		h.mu.Unlock()
		cancel()
		return nil, brokerJiraAttachmentFailure(err)
	}
	h.body = body
	h.state = brokerJiraAttachmentOpen
	reader := &brokerJiraAttachmentReader{handle: h, body: body}
	close(openDone)
	h.mu.Unlock()
	return reader, nil
}

func (h *brokerJiraAttachmentHandle) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	switch h.state {
	case brokerJiraAttachmentClosed:
		err := h.closeErr
		h.mu.Unlock()
		return err
	case brokerJiraAttachmentClosing:
		done := h.closeDone
		h.mu.Unlock()
		<-done
		h.mu.Lock()
		err := h.closeErr
		h.mu.Unlock()
		return err
	}
	prior := h.state
	h.state = brokerJiraAttachmentClosing
	h.uri = ""
	cancel, body, openDone := h.cancel, h.body, h.openDone
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if prior == brokerJiraAttachmentOpening {
		<-openDone
	}
	var closeErr error
	if body != nil {
		closeErr = brokerJiraAttachmentFailure(body.Close())
	}
	h.mu.Lock()
	h.body, h.cancel = nil, nil
	h.state = brokerJiraAttachmentClosed
	h.closeErr = closeErr
	close(h.closeDone)
	h.mu.Unlock()
	return closeErr
}

func (h *brokerJiraAttachmentHandle) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "Jira attachment handle")
}

type brokerJiraAttachmentReader struct {
	handle *brokerJiraAttachmentHandle
	body   io.ReadCloser
}

func (r *brokerJiraAttachmentReader) Read(buffer []byte) (int, error) {
	if r == nil || r.body == nil {
		return 0, brokerJiraAttachmentFailure(domain.ErrCheckFailed)
	}
	n, err := r.body.Read(buffer)
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, brokerJiraAttachmentFailure(err)
}

func (r *brokerJiraAttachmentReader) Close() error {
	if r == nil || r.handle == nil {
		return nil
	}
	return r.handle.Close()
}

func (r *brokerJiraAttachmentReader) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "Jira attachment reader")
}

type brokerJiraAttachmentError struct {
	cause  error
	status int
}

func (*brokerJiraAttachmentError) Error() string     { return "Jira broker attachment failed" }
func (e *brokerJiraAttachmentError) Unwrap() error   { return e.cause }
func (e *brokerJiraAttachmentError) HTTPStatus() int { return e.status }
func (e *brokerJiraAttachmentError) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "Jira broker attachment failed")
}

func brokerJiraAttachmentFailure(err error) error {
	if err == nil {
		return nil
	}
	var already *brokerJiraAttachmentError
	if errors.As(err, &already) {
		return already
	}
	causes := make([]error, 0, 4)
	for _, sentinel := range []error{
		domain.ErrReadDispatchExpired, domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted,
		domain.ErrUsage, domain.ErrAuth, domain.ErrForbidden, domain.ErrNotFound, domain.ErrVersionConflict, domain.ErrConfig, domain.ErrCheckFailed,
		context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF,
	} {
		if errors.Is(err, sentinel) {
			causes = append(causes, sentinel)
		}
	}
	result := &brokerJiraAttachmentError{cause: errors.Join(causes...)}
	var status interface{ HTTPStatus() int }
	if errors.As(err, &status) {
		result.status = status.HTTPStatus()
	}
	return result
}

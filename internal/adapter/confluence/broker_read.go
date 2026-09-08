package confluence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/isukharev/atl/internal/backendid"
	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/strictjson"
)

const (
	brokerPageQualificationBytes = 64 << 10
	brokerPageBusinessBytes      = 64 << 20
	brokerPageMaxAncestors       = 16
)

var _ domain.BrokerConfluencePageReadPort = (*Confluence)(nil)

// BrokerOriginSHA256 derives the contract digest from this adapter's immutable
// configured base. It performs no backend I/O and never returns the URL.
func (cf *Confluence) BrokerOriginSHA256() (string, error) {
	if cf == nil {
		return "", fmt.Errorf("invalid Confluence broker reader")
	}
	digest, err := backendid.OriginSHA256(cf.base)
	return strings.TrimPrefix(digest, backendid.Prefix), err
}

// QualifyBrokerPage requests only the current page's authorization metadata.
// Unavoidable default response fields are validated, then discarded.
func (cf *Confluence) QualifyBrokerPage(ctx context.Context, id string) (domain.BrokerConfluencePageIdentity, error) {
	page, err := cf.readBrokerPage(ctx, id, domain.BrokerConfluenceProjectionMetadata, brokerPageQualificationBytes, false)
	if err != nil {
		return domain.BrokerConfluencePageIdentity{}, err
	}
	return page.Identity, nil
}

// ReadBrokerPage performs one exact business read through the server-owned
// client. Native storage bytes are decoded from JSON without CSF conversion.
func (cf *Confluence) ReadBrokerPage(ctx context.Context, id string, projection domain.BrokerConfluenceProjection) (domain.BrokerConfluencePageSnapshot, error) {
	return cf.readBrokerPage(ctx, id, projection, brokerPageBusinessBytes, true)
}

func (cf *Confluence) readBrokerPage(ctx context.Context, id string, projection domain.BrokerConfluenceProjection, maximum int64, requireTitle bool) (domain.BrokerConfluencePageSnapshot, error) {
	if !domain.ValidConfluenceContentID(id) || projection != domain.BrokerConfluenceProjectionMetadata && projection != domain.BrokerConfluenceProjectionStorage {
		return domain.BrokerConfluencePageSnapshot{}, fmt.Errorf("%w: invalid exact Confluence broker read", domain.ErrUsage)
	}
	expand := "space,version,ancestors"
	if projection == domain.BrokerConfluenceProjectionStorage {
		expand += ",body.storage"
	}
	query := url.Values{"status": {"current"}, "expand": {expand}}
	ctx = domain.WithRedactedHTTPTrace(domain.WithSingleAttempt(ctx))
	raw, err := cf.c.DoWithBodyLimit(ctx, http.MethodGet, "/rest/api/content/"+id+"?"+query.Encode(), nil, nil, maximum)
	if err != nil {
		return domain.BrokerConfluencePageSnapshot{}, brokerPageTransportFailure(err)
	}
	return decodeBrokerPage(raw, id, projection, requireTitle)
}

// These DTOs deliberately admit only the selected expansions and bounded
// default metadata. They do not reuse the broad page-pull response shape.
type brokerPageWire struct {
	ID         string                `json:"id"`
	Type       string                `json:"type"`
	Status     string                `json:"status"`
	Title      string                `json:"title"`
	Space      *brokerPageSpace      `json:"space"`
	Version    *brokerPageVersion    `json:"version"`
	Ancestors  *[]brokerPageAncestor `json:"ancestors"`
	Body       *brokerPageBody       `json:"body"`
	Links      *brokerPageLinks      `json:"_links"`
	Expandable *brokerPageExpandable `json:"_expandable"`
}

type brokerPageSpace struct {
	ID         *int64                `json:"id"`
	Key        string                `json:"key"`
	Name       string                `json:"name"`
	Type       string                `json:"type"`
	Status     string                `json:"status"`
	Links      *brokerPageLinks      `json:"_links"`
	Expandable *brokerPageExpandable `json:"_expandable"`
}

type brokerPageVersion struct {
	Number     int                   `json:"number"`
	When       string                `json:"when"`
	By         *brokerPageUser       `json:"by"`
	Message    string                `json:"message"`
	MinorEdit  bool                  `json:"minorEdit"`
	Hidden     bool                  `json:"hidden"`
	Links      *brokerPageLinks      `json:"_links"`
	Expandable *brokerPageExpandable `json:"_expandable"`
}

type brokerPageAncestor struct {
	ID         string                `json:"id"`
	Type       *string               `json:"type"`
	Status     *string               `json:"status"`
	Title      string                `json:"title"`
	Links      *brokerPageLinks      `json:"_links"`
	Expandable *brokerPageExpandable `json:"_expandable"`
}

type brokerPageUser struct {
	Type           string                    `json:"type"`
	Username       string                    `json:"username"`
	UserKey        string                    `json:"userKey"`
	AccountID      string                    `json:"accountId"`
	DisplayName    string                    `json:"displayName"`
	Email          string                    `json:"email"`
	ProfilePicture *brokerPageProfilePicture `json:"profilePicture"`
	Links          *brokerPageLinks          `json:"_links"`
	Expandable     *brokerPageExpandable     `json:"_expandable"`
}

type brokerPageProfilePicture struct {
	Path      string `json:"path"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	IsDefault bool   `json:"isDefault"`
}

type brokerPageLinks struct {
	Self       string `json:"self"`
	Base       string `json:"base"`
	Context    string `json:"context"`
	WebUI      string `json:"webui"`
	TinyUI     string `json:"tinyui"`
	EditUI     string `json:"editui"`
	Collection string `json:"collection"`
}

// Expansion references are inert strings, never URLs to follow. Actual
// expansions outside brokerPageWire fail closed even when advertised here.
type brokerPageExpandable struct {
	Space               string `json:"space"`
	Version             string `json:"version"`
	Ancestors           string `json:"ancestors"`
	Body                string `json:"body"`
	History             string `json:"history"`
	Children            string `json:"children"`
	Descendants         string `json:"descendants"`
	Metadata            string `json:"metadata"`
	Restrictions        string `json:"restrictions"`
	Operations          string `json:"operations"`
	Container           string `json:"container"`
	Homepage            string `json:"homepage"`
	Description         string `json:"description"`
	Icon                string `json:"icon"`
	Settings            string `json:"settings"`
	Content             string `json:"content"`
	PersonalSpace       string `json:"personalSpace"`
	ChildTypes          string `json:"childTypes"`
	SchedulePublishDate string `json:"schedulePublishDate"`
}

type brokerPageBody struct {
	Storage    *brokerPageStorage        `json:"storage"`
	Expandable *brokerPageBodyExpandable `json:"_expandable"`
}

type brokerPageStorage struct {
	Value          *brokerPageNativeString `json:"value"`
	Representation string                  `json:"representation"`
	Expandable     *brokerPageExpandable   `json:"_expandable"`
}

type brokerPageBodyExpandable struct {
	Storage             string `json:"storage"`
	View                string `json:"view"`
	Editor              string `json:"editor"`
	ExportView          string `json:"export_view"`
	StyledView          string `json:"styled_view"`
	AnonymousExportView string `json:"anonymous_export_view"`
	AtlasDocFormat      string `json:"atlas_doc_format"`
}

type brokerPageNativeString string

func decodeBrokerPage(raw []byte, id string, projection domain.BrokerConfluenceProjection, requireTitle bool) (domain.BrokerConfluencePageSnapshot, error) {
	failure := fmt.Errorf("%w: incomplete or unsupported Confluence broker page evidence", domain.ErrCheckFailed)
	var page brokerPageWire
	if strictjson.ValidateNestingDepth(raw, 64) != nil || strictjson.Validate(raw) != nil ||
		!brokerPageClosedShape(raw, reflect.TypeFor[brokerPageWire]()) || json.Unmarshal(raw, &page) != nil {
		return domain.BrokerConfluencePageSnapshot{}, failure
	}
	if page.ID != id || !domain.ValidConfluenceContentID(page.ID) || page.Type != "page" || page.Status != "current" ||
		requireTitle && !brokerPageTitle(page.Title) || page.Space == nil || !brokerPageIdentifier(page.Space.Key) || page.Space.ID != nil && *page.Space.ID <= 0 ||
		page.Version == nil || page.Version.Number <= 0 || !brokerPageUpdated(page.Version.When) || page.Ancestors == nil {
		return domain.BrokerConfluencePageSnapshot{}, failure
	}
	if page.Expandable != nil && (page.Expandable.Space != "" || page.Expandable.Version != "" || page.Expandable.Ancestors != "") {
		return domain.BrokerConfluencePageSnapshot{}, failure
	}
	ancestors := make([]string, 0, len(*page.Ancestors))
	seen := map[string]bool{page.ID: true}
	for _, ancestor := range *page.Ancestors {
		if !domain.ValidConfluenceContentID(ancestor.ID) || seen[ancestor.ID] ||
			ancestor.Type != nil && *ancestor.Type != "page" || ancestor.Status != nil && *ancestor.Status != "current" {
			return domain.BrokerConfluencePageSnapshot{}, failure
		}
		seen[ancestor.ID] = true
		ancestors = append(ancestors, ancestor.ID)
	}
	result := domain.BrokerConfluencePageSnapshot{
		Identity: domain.BrokerConfluencePageIdentity{
			ID: page.ID, Type: page.Type, Status: page.Status, Space: page.Space.Key,
			Version: page.Version.Number, Updated: page.Version.When, AncestorIDs: ancestors,
			AncestorsPresent: true, Complete: true,
		},
		Title: page.Title, Projection: projection, Complete: true,
	}
	if projection == domain.BrokerConfluenceProjectionMetadata {
		if page.Body != nil {
			return domain.BrokerConfluencePageSnapshot{}, failure
		}
		return result, nil
	}
	if page.Body == nil || page.Body.Storage == nil || page.Body.Storage.Value == nil || page.Body.Storage.Representation != "storage" ||
		page.Expandable != nil && page.Expandable.Body != "" || page.Body.Expandable != nil && page.Body.Expandable.Storage != "" {
		return domain.BrokerConfluencePageSnapshot{}, failure
	}
	result.Storage = []byte(*page.Body.Storage.Value)
	result.StoragePresent = true
	return result, nil
}

// brokerPageClosedShape supplements typed decoding with exact member spelling,
// non-null presence, and per-value bounds. JSON's default case folding and null
// coercion must not establish authorization evidence.
func brokerPageClosedShape(raw json.RawMessage, typ reflect.Type) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return false
		}
		for key, value := range object {
			found := false
			for index := range typ.NumField() {
				field := typ.Field(index)
				if key == field.Tag.Get("json") {
					found = brokerPageClosedShape(value, field.Type)
					break
				}
			}
			if !found {
				return false
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || values == nil || len(values) > brokerPageMaxAncestors {
			return false
		}
		for _, value := range values {
			if !brokerPageClosedShape(value, typ.Elem()) {
				return false
			}
		}
	case reflect.String:
		var value string
		maximum := brokerPageQualificationBytes
		if typ == reflect.TypeFor[brokerPageNativeString]() {
			maximum = brokerPageBusinessBytes
		}
		if json.Unmarshal(raw, &value) != nil || len(value) > maximum {
			return false
		}
	}
	return true
}

func brokerPageIdentifier(value string) bool {
	if value == "" || len(value) > domain.BrokerMaxIdentifierBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

func brokerPageTitle(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < 0x20 && char != '\t' && char != '\r' && char != '\n' {
			return false
		}
	}
	return true
}

func brokerPageUpdated(value string) bool {
	if !brokerPageIdentifier(value) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

type brokerPageReadError struct {
	cause  error
	status int
}

func (*brokerPageReadError) Error() string     { return "Confluence broker page read failed" }
func (e *brokerPageReadError) Unwrap() error   { return e.cause }
func (e *brokerPageReadError) HTTPStatus() int { return e.status }

func brokerPageTransportFailure(err error) error {
	var causes []error
	for _, sentinel := range []error{
		domain.ErrUsage, domain.ErrAuth, domain.ErrForbidden, domain.ErrNotFound,
		domain.ErrVersionConflict, domain.ErrConfig, domain.ErrCheckFailed,
		domain.ErrReadAttemptBudgetExhausted, domain.ErrReadResponseBudgetExhausted,
		context.Canceled, context.DeadlineExceeded,
	} {
		if errors.Is(err, sentinel) {
			causes = append(causes, sentinel)
		}
	}
	result := &brokerPageReadError{cause: errors.Join(causes...)}
	var status interface{ HTTPStatus() int }
	if errors.As(err, &status) {
		result.status = status.HTTPStatus()
	}
	return result
}

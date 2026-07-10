package app

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/isukharev/atl/internal/domain"
	"github.com/isukharev/atl/internal/mirror"
	"github.com/isukharev/atl/internal/safepath"
)

const jiraPendingFieldsVersion = 2

type JiraPendingField struct {
	ID    string `json:"id"`
	Base  string `json:"base"`
	Value string `json:"value"`
}

type JiraPendingFields struct {
	Version        int                `json:"version"`
	Key            string             `json:"key"`
	WikiPath       string             `json:"wiki_path"`
	WikiHash       string             `json:"wiki_hash"`
	WikiBody       string             `json:"wiki_body"`
	BeforeWikiHash string             `json:"before_wiki_hash,omitempty"`
	Fields         []JiraPendingField `json:"fields"`
}

func jiraPendingFieldsPath(root, key string) string {
	return filepath.Join(root, ".atl", "pending", "jira", safepath.Segment(key)+".json")
}

func jiraPendingFieldsTxnPath(root, key string) string {
	return filepath.Join(root, ".atl", "pending", "jira", "."+safepath.Segment(key)+".txn.json")
}

func jiraPendingFieldsLockPath(root string) string {
	return filepath.Join(root, ".atl", "pending", "jira", ".mirror.lock")
}

func lockJiraPendingFields(root, key string) (*safepath.FileLock, error) {
	path := jiraPendingFieldsLockPath(root)
	if err := safepath.MkdirAllWithin(root, filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	lock, acquired, err := safepath.TryLockFileWithin(root, path, 0o600)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("%w: Jira apply/push is already active for %s", domain.ErrCheckFailed, key)
	}
	return lock, nil
}

func loadJiraPendingFields(root, key string) (*JiraPendingFields, bool, error) {
	lock, err := lockJiraPendingFields(root, key)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = lock.Unlock() }()
	return loadJiraPendingFieldsLocked(root, key)
}

func loadJiraPendingFieldsLocked(root, key string) (*JiraPendingFields, bool, error) {
	if err := recoverJiraPendingTransaction(root, key); err != nil {
		return nil, false, err
	}
	path := jiraPendingFieldsPath(root, key)
	b, err := safepath.ReadFileWithin(root, path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var pending JiraPendingFields
	if err := json.Unmarshal(b, &pending); err != nil {
		return nil, false, fmt.Errorf("%w: corrupt pending Jira fields %s: %v", domain.ErrCheckFailed, path, err)
	}
	if pending.Version != jiraPendingFieldsVersion || pending.Key != key || pending.WikiPath == "" || pending.WikiHash == "" || mirror.Hash([]byte(pending.WikiBody)) != pending.WikiHash {
		return nil, false, fmt.Errorf("%w: invalid pending Jira fields identity/version in %s", domain.ErrCheckFailed, path)
	}
	if err := validateJiraPendingFields(root, path, &pending); err != nil {
		return nil, false, err
	}
	return &pending, true, nil
}

func saveJiraPendingFields(root string, pending *JiraPendingFields) error {
	if pending == nil || len(pending.Fields) == 0 {
		if pending == nil || pending.Key == "" {
			return nil
		}
		err := safepath.RemoveWithin(root, jiraPendingFieldsPath(root, pending.Key))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if pending.WikiHash == "" || mirror.Hash([]byte(pending.WikiBody)) != pending.WikiHash {
		return fmt.Errorf("%w: pending Jira fields are missing the reviewed wiki hash", domain.ErrCheckFailed)
	}
	pending.Version = jiraPendingFieldsVersion
	sort.Slice(pending.Fields, func(i, j int) bool { return pending.Fields[i].ID < pending.Fields[j].ID })
	path := jiraPendingFieldsPath(root, pending.Key)
	if err := validateJiraPendingFields(root, path, pending); err != nil {
		return err
	}
	b, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	if err := safepath.MkdirAllWithin(root, filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return safepath.WriteFileWithin(root, path, append(b, '\n'), 0o600)
}

func stageJiraPendingTransaction(root string, pending *JiraPendingFields) error {
	if pending == nil || pending.Key == "" || pending.WikiHash == "" || mirror.Hash([]byte(pending.WikiBody)) != pending.WikiHash || pending.BeforeWikiHash == "" {
		return fmt.Errorf("%w: incomplete pending Jira transaction", domain.ErrCheckFailed)
	}
	pending.Version = jiraPendingFieldsVersion
	sort.Slice(pending.Fields, func(i, j int) bool { return pending.Fields[i].ID < pending.Fields[j].ID })
	path := jiraPendingFieldsTxnPath(root, pending.Key)
	if err := validateJiraPendingFields(root, path, pending); err != nil {
		return err
	}
	b, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	if err := safepath.MkdirAllWithin(root, filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return safepath.WriteFileWithin(root, path, append(b, '\n'), 0o600)
}

func commitJiraPendingTransaction(root string, pending *JiraPendingFields) error {
	txnPath := jiraPendingFieldsTxnPath(root, pending.Key)
	if len(pending.Fields) == 0 {
		if err := safepath.RemoveWithin(root, jiraPendingFieldsPath(root, pending.Key)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return safepath.RemoveWithin(root, txnPath)
	}
	return safepath.RenameWithin(root, txnPath, jiraPendingFieldsPath(root, pending.Key))
}

func recoverJiraPendingTransaction(root, key string) error {
	path := jiraPendingFieldsTxnPath(root, key)
	b, err := safepath.ReadFileWithin(root, path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var txn JiraPendingFields
	if err := json.Unmarshal(b, &txn); err != nil || txn.Version != jiraPendingFieldsVersion || txn.Key != key || txn.BeforeWikiHash == "" || txn.WikiHash == "" {
		return fmt.Errorf("%w: corrupt pending Jira transaction %s", domain.ErrCheckFailed, path)
	}
	if err := validateJiraPendingFields(root, path, &txn); err != nil {
		return err
	}
	wiki, err := safepath.ReadFileWithin(root, filepath.Join(root, txn.WikiPath))
	if err != nil {
		return fmt.Errorf("%w: recover pending Jira transaction %s: %v", domain.ErrCheckFailed, key, err)
	}
	currentHash := mirror.Hash(wiki)
	// Field-only apply intentionally leaves .wiki byte-identical, so before and
	// after hashes are equal. The complete exclusive txn is itself the reviewed
	// commit in that case and is safe to promote.
	if txn.BeforeWikiHash == txn.WikiHash && currentHash == txn.WikiHash {
		return commitJiraPendingTransaction(root, &txn)
	}
	switch currentHash {
	case txn.BeforeWikiHash:
		return safepath.RemoveWithin(root, path)
	case txn.WikiHash:
		return commitJiraPendingTransaction(root, &txn)
	default:
		return fmt.Errorf("%w: pending Jira transaction %s does not match either the pre-apply or reviewed wiki hash", domain.ErrCheckFailed, key)
	}
}

func recoverAllJiraPendingTransactionsLocked(root string) error {
	dir := filepath.Join(root, ".atl", "pending", "jira")
	entries, err := safepath.ReadDirWithin(root, dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".txn.json") {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(name, "."), ".txn.json")
		if err := recoverJiraPendingTransaction(root, key); err != nil {
			return err
		}
	}
	return nil
}

func listJiraPendingFields(root string) ([]JiraPendingFields, error) {
	lock, err := lockJiraPendingFields(root, "list")
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	if err := recoverAllJiraPendingTransactionsLocked(root); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, ".atl", "pending", "jira")
	entries, err := safepath.ReadDirWithin(root, dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []JiraPendingFields
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := safepath.ReadFileWithin(root, filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var pending JiraPendingFields
		if err := json.Unmarshal(b, &pending); err != nil || pending.Version != jiraPendingFieldsVersion || pending.Key == "" || pending.WikiPath == "" || pending.WikiHash == "" || mirror.Hash([]byte(pending.WikiBody)) != pending.WikiHash {
			return nil, fmt.Errorf("%w: corrupt pending Jira fields %s", domain.ErrCheckFailed, filepath.Join(dir, entry.Name()))
		}
		path := filepath.Join(dir, entry.Name())
		if safepath.Segment(pending.Key)+".json" != entry.Name() {
			return nil, fmt.Errorf("%w: pending Jira field filename does not match its key in %s", domain.ErrCheckFailed, path)
		}
		if err := validateJiraPendingFields(root, path, &pending); err != nil {
			return nil, err
		}
		out = append(out, pending)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func validateJiraPendingFields(root, path string, pending *JiraPendingFields) error {
	if pending.WikiHash == "" || mirror.Hash([]byte(pending.WikiBody)) != pending.WikiHash {
		return fmt.Errorf("%w: pending Jira wiki body/hash mismatch in %s", domain.ErrCheckFailed, path)
	}
	clean := filepath.Clean(pending.WikiPath)
	if filepath.IsAbs(pending.WikiPath) || clean != pending.WikiPath || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.Ext(clean) != wikiExt {
		return fmt.Errorf("%w: invalid pending Jira wiki path in %s", domain.ErrCheckFailed, path)
	}
	if strings.TrimSuffix(filepath.Base(clean), wikiExt) != safepath.Segment(pending.Key) {
		return fmt.Errorf("%w: pending Jira wiki path does not match its key in %s", domain.ErrCheckFailed, path)
	}
	if !safepath.Within(root, filepath.Join(root, clean)) {
		return fmt.Errorf("%w: pending Jira wiki path escapes the mirror in %s", domain.ErrCheckFailed, path)
	}
	seen := map[string]bool{}
	for _, field := range pending.Fields {
		if strings.TrimSpace(field.ID) == "" || field.ID == "description" || seen[field.ID] {
			return fmt.Errorf("%w: invalid or duplicate pending field id in %s", domain.ErrCheckFailed, path)
		}
		seen[field.ID] = true
	}
	return nil
}

func jiraPendingFieldIDs(pending *JiraPendingFields) []string {
	if pending == nil {
		return nil
	}
	ids := make([]string, 0, len(pending.Fields))
	for _, field := range pending.Fields {
		ids = append(ids, field.ID)
	}
	sort.Strings(ids)
	return ids
}

func validatePendingFieldsEditable(pending *JiraPendingFields, rs RenderSettings) error {
	if pending == nil {
		return nil
	}
	if !rs.On(SecCustomFields) {
		return fmt.Errorf("%w: pending Jira fields require the custom_fields render section to remain enabled", domain.ErrCheckFailed)
	}
	allowed := make(map[string]bool, len(rs.FieldViews))
	for _, view := range rs.FieldViews {
		if view.Editable && view.Placement == "section" && view.Format == "jira_wiki" {
			allowed[view.ID] = true
		}
	}
	for _, field := range pending.Fields {
		if !allowed[field.ID] {
			return fmt.Errorf("%w: pending Jira field %s is not editable in the recorded render view; restore the descriptor or discard/reconcile the pending edit", domain.ErrCheckFailed, field.ID)
		}
	}
	return nil
}

func validatePendingMirrorBinding(root string, pending *JiraPendingFields, lw *mirror.LocalWiki, wiki []byte) error {
	if pending == nil {
		return nil
	}
	if lw == nil || lw.Synced == nil {
		return fmt.Errorf("%w: pending Jira fields for %s have no synced mirror entry", domain.ErrCheckFailed, pending.Key)
	}
	rel, err := filepath.Rel(root, lw.Path)
	if err != nil || filepath.Clean(rel) != filepath.Clean(pending.WikiPath) || filepath.Clean(lw.Synced.Path) != filepath.Clean(pending.WikiPath) {
		return fmt.Errorf("%w: pending Jira fields for %s do not match the recorded mirror path", domain.ErrCheckFailed, pending.Key)
	}
	if mirror.Hash(wiki) != pending.WikiHash {
		return fmt.Errorf("%w: pending Jira fields for %s do not match the reviewed local wiki hash; review both edits, then run jira apply --rebase-pending to bind them explicitly", domain.ErrCheckFailed, pending.Key)
	}
	return nil
}

func jiraWikiHasHash(root, wikiPath, want string) (bool, error) {
	wiki, err := safepath.ReadFileWithin(root, wikiPath)
	if err != nil {
		return false, err
	}
	return mirror.Hash(wiki) == want, nil
}

func pendingFieldMap(pending *JiraPendingFields) map[string]JiraPendingField {
	out := map[string]JiraPendingField{}
	if pending != nil {
		for _, field := range pending.Fields {
			out[field.ID] = field
		}
	}
	return out
}

func issueWithPendingFields(issue *domain.Issue, pending *JiraPendingFields) *domain.Issue {
	if issue == nil || pending == nil || len(pending.Fields) == 0 {
		return issue
	}
	copyIssue := *issue
	copyIssue.Fields = maps.Clone(issue.Fields)
	if copyIssue.Fields == nil {
		copyIssue.Fields = map[string]any{}
	}
	for _, field := range pending.Fields {
		copyIssue.Fields[field.ID] = field.Value
	}
	return &copyIssue
}

package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

type StructureFolderSelector struct {
	FolderID   string
	FolderRow  int64
	FolderPath string
}

type StructureSelection struct {
	Kind     string   `json:"kind"`
	FolderID string   `json:"folder_id"`
	RowID    int64    `json:"row_id"`
	Path     []string `json:"path"`
}

type StructureFolderStats struct {
	DescendantRows   int `json:"descendant_rows"`
	IssueRows        int `json:"issue_rows"`
	UniqueIssues     int `json:"unique_issues"`
	Subfolders       int `json:"subfolders"`
	MaxRelativeDepth int `json:"max_relative_depth"`
}

type StructureFolder struct {
	FolderID       string               `json:"folder_id"`
	RowID          int64                `json:"row_id"`
	Name           string               `json:"name"`
	Path           []string             `json:"path"`
	Depth          int                  `json:"depth"`
	ParentFolderID string               `json:"parent_folder_id"`
	Stats          StructureFolderStats `json:"stats"`
}

type StructureFoldersResult struct {
	SchemaVersion int                       `json:"schema_version"`
	Structure     StructureSnapshotMetadata `json:"structure"`
	ForestVersion domain.StructureVersion   `json:"forest_version"`
	Folders       []StructureFolder         `json:"folders"`
	Complete      bool                      `json:"complete"`
	Warnings      []string                  `json:"warnings"`
}

func selectorCount(selector StructureFolderSelector) int {
	count := 0
	if strings.TrimSpace(selector.FolderID) != "" {
		count++
	}
	if selector.FolderRow != 0 {
		count++
	}
	if strings.TrimSpace(selector.FolderPath) != "" {
		count++
	}
	return count
}

func validateStructureSelector(root string, selector StructureFolderSelector) error {
	count := selectorCount(selector)
	if strings.TrimSpace(root) != "" {
		count++
	}
	if count > 1 {
		return fmt.Errorf("%w: --root, --folder-id, --folder-row, and --folder-path are mutually exclusive", domain.ErrUsage)
	}
	if selector.FolderRow < 0 {
		return fmt.Errorf("%w: --folder-row must be positive", domain.ErrUsage)
	}
	return nil
}

func (s *JiraService) structureFolderLabelsChecked(ctx context.Context, id int64, rows []domain.StructureRow) (map[int64]string, bool, []string) {
	labels := map[int64]string{}
	folderRows := []int64{}
	for _, row := range rows {
		if structureItemTypeLabel(row.ItemType) == "folder" {
			folderRows = append(folderRows, row.RowID)
		}
	}
	if len(folderRows) == 0 {
		return labels, true, []string{}
	}
	values, err := s.StructureValues(ctx, id, folderRows, []string{"key", "summary"})
	if err != nil {
		return labels, false, []string{"Structure folder labels could not be read; stable ids and statistics remain available"}
	}
	normalized, _, err := normalizeStructureValueRows(values)
	if err != nil {
		return labels, false, []string{"Structure folder labels could not be normalized; stable ids and statistics remain available"}
	}
	missing := 0
	for _, rowID := range folderRows {
		label := snapshotText(normalized[rowID]["summary"])
		if label == "" {
			label = snapshotText(normalized[rowID]["key"])
		}
		if label == "" {
			missing++
			continue
		}
		labels[rowID] = label
	}
	if missing > 0 {
		return labels, false, []string{fmt.Sprintf("Structure folder labels are unavailable for %d of %d folders", missing, len(folderRows))}
	}
	return labels, true, []string{}
}

func buildStructureFolders(rows []domain.StructureRow, labels map[int64]string) []StructureFolder {
	folders := []StructureFolder{}
	stack := []int{}
	for rowIndex, row := range rows {
		for len(stack) > 0 && folders[stack[len(stack)-1]].Depth >= row.Depth {
			stack = stack[:len(stack)-1]
		}
		if structureItemTypeLabel(row.ItemType) != "folder" {
			continue
		}
		name := labels[row.RowID]
		pathPart := name
		if pathPart == "" {
			pathPart = "folder:" + row.ItemID
		}
		path := []string{pathPart}
		parentID := ""
		if len(stack) > 0 {
			parent := folders[stack[len(stack)-1]]
			path = append(append([]string{}, parent.Path...), pathPart)
			parentID = parent.FolderID
		}
		stats := structureFolderStats(rows, rowIndex)
		folders = append(folders, StructureFolder{FolderID: row.ItemID, RowID: row.RowID, Name: name, Path: path, Depth: row.Depth, ParentFolderID: parentID, Stats: stats})
		stack = append(stack, len(folders)-1)
	}
	return folders
}

func structureFolderStats(rows []domain.StructureRow, index int) StructureFolderStats {
	root := rows[index]
	stats := StructureFolderStats{}
	unique := map[string]bool{}
	for i := index + 1; i < len(rows) && rows[i].Depth > root.Depth; i++ {
		row := rows[i]
		stats.DescendantRows++
		relative := row.Depth - root.Depth
		if relative > stats.MaxRelativeDepth {
			stats.MaxRelativeDepth = relative
		}
		if row.ItemType == "issue" {
			stats.IssueRows++
			unique[row.ItemID] = true
		}
		if structureItemTypeLabel(row.ItemType) == "folder" {
			stats.Subfolders++
		}
	}
	stats.UniqueIssues = len(unique)
	return stats
}

func (s *JiraService) StructureFolders(ctx context.Context, id int64) (*StructureFoldersResult, error) {
	metadata, err := s.Structure(ctx, id)
	if err != nil {
		return nil, err
	}
	forest, err := s.StructureForest(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := ParseStructureRows(forest)
	if err != nil {
		return nil, err
	}
	labels, complete, warnings := s.structureFolderLabelsChecked(ctx, id, rows)
	return &StructureFoldersResult{
		SchemaVersion: 1,
		Structure:     StructureSnapshotMetadata{ID: metadata.ID, Name: metadata.Name, ReadOnly: metadata.ReadOnly},
		ForestVersion: forest.Version, Folders: buildStructureFolders(rows, labels), Complete: complete, Warnings: warnings,
	}, nil
}

func selectStructureFolder(rows []domain.StructureRow, folders []StructureFolder, complete bool, selector StructureFolderSelector) ([]domain.StructureRow, *StructureSelection, error) {
	if selectorCount(selector) == 0 {
		return rows, nil, nil
	}
	matches := []StructureFolder{}
	switch {
	case strings.TrimSpace(selector.FolderID) != "":
		for _, folder := range folders {
			if folder.FolderID == strings.TrimSpace(selector.FolderID) {
				matches = append(matches, folder)
			}
		}
	case selector.FolderRow != 0:
		for _, folder := range folders {
			if folder.RowID == selector.FolderRow {
				matches = append(matches, folder)
			}
		}
		if len(matches) == 0 {
			for _, row := range rows {
				if row.RowID == selector.FolderRow && structureItemTypeLabel(row.ItemType) != "folder" {
					return nil, nil, fmt.Errorf("%w: Structure row %d is not a stored folder", domain.ErrUsage, selector.FolderRow)
				}
			}
		}
	case strings.TrimSpace(selector.FolderPath) != "":
		if !complete {
			return nil, nil, fmt.Errorf("%w: exact folder path cannot be validated because folder labels are incomplete; use --folder-id or --folder-row", domain.ErrCheckFailed)
		}
		wanted, err := normalizeFolderPath(selector.FolderPath)
		if err != nil {
			return nil, nil, err
		}
		for _, folder := range folders {
			if normalizedFolderParts(folder.Path) == wanted {
				matches = append(matches, folder)
			}
		}
	}
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("%w: exact Structure folder was not found", domain.ErrNotFound)
	}
	if len(matches) > 1 {
		ids := make([]string, len(matches))
		for i, match := range matches {
			ids[i] = fmt.Sprintf("folder=%s row=%d", match.FolderID, match.RowID)
		}
		return nil, nil, fmt.Errorf("%w: exact Structure folder selector is ambiguous: %s", domain.ErrCheckFailed, strings.Join(ids, ", "))
	}
	match := matches[0]
	start := -1
	for i, row := range rows {
		if row.RowID == match.RowID {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, nil, fmt.Errorf("%w: selected Structure folder row disappeared from the forest snapshot", domain.ErrCheckFailed)
	}
	rootDepth := rows[start].Depth
	end := start + 1
	for end < len(rows) && rows[end].Depth > rootDepth {
		end++
	}
	selected := append([]domain.StructureRow(nil), rows[start:end]...)
	baseDepth := rootDepth
	for i := range selected {
		relative := selected[i].Depth - baseDepth
		selected[i].RelativeDepth = &relative
	}
	kind := "folder-id"
	if selector.FolderRow != 0 {
		kind = "folder-row"
	} else if strings.TrimSpace(selector.FolderPath) != "" {
		kind = "folder-path"
	}
	selection := &StructureSelection{Kind: kind, FolderID: match.FolderID, RowID: match.RowID, Path: append([]string{}, match.Path...)}
	return selected, selection, nil
}

func normalizeFolderPath(path string) (string, error) {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return "", fmt.Errorf("%w: --folder-path must contain at least one segment", domain.ErrUsage)
	}
	normalized := make([]string, len(parts))
	for i, part := range parts {
		part = strings.Join(strings.Fields(part), " ")
		if part == "" {
			return "", fmt.Errorf("%w: --folder-path contains an empty segment", domain.ErrUsage)
		}
		normalized[i] = strings.ToLower(part)
	}
	return strings.Join(normalized, "/"), nil
}

func normalizedFolderParts(parts []string) string {
	normalized := make([]string, len(parts))
	for i, part := range parts {
		normalized[i] = strings.ToLower(strings.Join(strings.Fields(part), " "))
	}
	return strings.Join(normalized, "/")
}

func StructureFoldersMarkdown(result *StructureFoldersResult) string {
	headers := []string{"Folder ID", "Path", "Depth", "Issue rows", "Unique issues", "Subfolders", "Rows"}
	rows := make([][]string, len(result.Folders))
	for i, folder := range result.Folders {
		rows[i] = []string{folder.FolderID, strings.Join(folder.Path, " / "), strconv.Itoa(folder.Depth), strconv.Itoa(folder.Stats.IssueRows), strconv.Itoa(folder.Stats.UniqueIssues), strconv.Itoa(folder.Stats.Subfolders), strconv.Itoa(folder.Stats.DescendantRows)}
	}
	return MarkdownTable(headers, rows)
}

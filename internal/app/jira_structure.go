package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/isukharev/atl/internal/domain"
)

// Structure fetches metadata for a Tempo Structure.
func (s *JiraService) Structure(ctx context.Context, id int64) (*domain.Structure, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: structure id must be positive", domain.ErrUsage)
	}
	return s.structure.GetStructure(ctx, id)
}

// StructureForest returns the latest raw forest formula for a Tempo Structure.
func (s *JiraService) StructureForest(ctx context.Context, id int64) (*domain.StructureForest, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: structure id must be positive", domain.ErrUsage)
	}
	return s.structure.StructureForest(ctx, id)
}

// StructureRows parses the latest forest formula into row records.
func (s *JiraService) StructureRows(ctx context.Context, id int64) ([]domain.StructureRow, *domain.StructureVersion, error) {
	forest, err := s.StructureForest(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rows, err := ParseStructureRows(forest)
	if err != nil {
		return nil, nil, err
	}
	return rows, &forest.Version, nil
}

// StructureValues fetches attribute values for selected Structure rows.
func (s *JiraService) StructureValues(ctx context.Context, id int64, rows []int64, fields []string) (*domain.StructureValues, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: structure id must be positive", domain.ErrUsage)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: pass at least one row id", domain.ErrUsage)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("%w: pass at least one field", domain.ErrUsage)
	}
	return s.structure.StructureValues(ctx, id, rows, fields)
}

// ParseStructureRows parses Structure's forest formula. A component has the
// documented shape rowID:depth:item[:semantic]. Issue rows use a numeric item id;
// non-issue rows use itemType/itemID or itemType//stringItemID.
func ParseStructureRows(forest *domain.StructureForest) ([]domain.StructureRow, error) {
	if forest == nil || strings.TrimSpace(forest.Formula) == "" {
		return nil, nil
	}
	parts := strings.Split(forest.Formula, ",")
	rows := make([]domain.StructureRow, 0, len(parts))
	depthStack := map[int]int64{}
	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		fields := strings.Split(raw, ":")
		if len(fields) < 3 {
			return nil, fmt.Errorf("%w: invalid structure formula component %q", domain.ErrUsage, raw)
		}
		rowID, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid structure row id %q", domain.ErrUsage, fields[0])
		}
		depth, err := strconv.Atoi(fields[1])
		if err != nil || depth < 0 {
			return nil, fmt.Errorf("%w: invalid structure row depth %q", domain.ErrUsage, fields[1])
		}
		itemType, itemID := parseStructureItem(fields[2], forest.ItemTypes)
		row := domain.StructureRow{
			RowID:    rowID,
			Depth:    depth,
			ItemType: itemType,
			ItemID:   itemID,
			Position: len(rows),
		}
		if len(fields) > 3 {
			row.Semantic = strings.Join(fields[3:], ":")
		}
		if depth > 0 {
			row.ParentRowID = depthStack[depth-1]
		}
		rows = append(rows, row)
		depthStack[depth] = rowID
		for staleDepth := range depthStack {
			if staleDepth > depth {
				delete(depthStack, staleDepth)
			}
		}
	}
	return rows, nil
}

func parseStructureItem(item string, itemTypes map[string]string) (string, string) {
	if _, err := strconv.ParseInt(item, 10, 64); err == nil {
		return "issue", item
	}
	typeID, itemID, ok := strings.Cut(item, "//")
	if !ok {
		typeID, itemID, ok = strings.Cut(item, "/")
	}
	if !ok {
		return "", item
	}
	itemType := typeID
	if itemTypes != nil && itemTypes[typeID] != "" {
		itemType = itemTypes[typeID]
	}
	return itemType, itemID
}

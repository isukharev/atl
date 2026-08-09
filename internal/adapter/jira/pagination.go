package jira

// jiraOffsetCursor owns checked offset arithmetic for Jira endpoints. Callers
// retain endpoint-specific total policy, error wording, and partial-result
// behavior while sharing one overflow-safe progress classifier.
type jiraOffsetCursor struct {
	startAt int
}

type jiraOffsetAdvance uint8

const (
	jiraOffsetMore jiraOffsetAdvance = iota
	jiraOffsetComplete
	jiraOffsetStalled
	jiraOffsetBeyondTotal
	jiraOffsetOverflow
)

type jiraOffsetDecision struct {
	state jiraOffsetAdvance
	next  int
}

func (c *jiraOffsetCursor) requested() int { return c.startAt }

func (c *jiraOffsetCursor) matches(returned int) bool { return returned == c.startAt }

func (c *jiraOffsetCursor) advance(rows int, total *int) jiraOffsetDecision {
	if rows < 0 || rows > int(^uint(0)>>1)-c.startAt {
		return jiraOffsetDecision{state: jiraOffsetOverflow, next: c.startAt}
	}
	next := c.startAt + rows
	if total != nil {
		if next > *total {
			return jiraOffsetDecision{state: jiraOffsetBeyondTotal, next: next}
		}
		if next == *total {
			return jiraOffsetDecision{state: jiraOffsetComplete, next: next}
		}
	}
	if rows == 0 {
		return jiraOffsetDecision{state: jiraOffsetStalled, next: next}
	}
	c.startAt = next
	return jiraOffsetDecision{state: jiraOffsetMore, next: next}
}

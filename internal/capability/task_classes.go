package capability

import "sort"

// TaskClasses returns every exact task class represented by the curated
// capability catalog in deterministic lexical order. The returned slice is a
// copy and may be modified by the caller.
func TaskClasses() []string {
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		seen[definition.TaskClass] = struct{}{}
	}
	classes := make([]string, 0, len(seen))
	for taskClass := range seen {
		classes = append(classes, taskClass)
	}
	sort.Strings(classes)
	return classes
}

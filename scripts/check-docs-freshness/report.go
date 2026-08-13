package main

import "strings"

type report struct {
	Commands         int
	Flags            int
	Routes           int
	TaskClasses      int
	MutationProfiles int
	ImpactRules      int
	SelectedChecks   []string
	PrivateMarkers   int
}

func selectedImpactChecks(root, base, head string, current impactManifest) ([]string, error) {
	if strings.TrimSpace(base) == "" {
		return nil, nil
	}
	changed, err := changedFiles(root, base, head)
	if err != nil {
		return nil, err
	}
	baseline, err := loadImpactManifestAtRevision(root, strings.TrimSpace(base))
	if err != nil {
		return nil, err
	}
	return classifyImpact(current, baseline, changed)
}

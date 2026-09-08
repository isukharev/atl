package main

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"

	"github.com/isukharev/atl/scripts/check-docs-freshness/hosted"
)

var hostedSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

func runHostedPlan(root, base, head string, full bool, out io.Writer) error {
	if !hostedSHA.MatchString(base) || !hostedSHA.MatchString(head) {
		return errors.New("hosted selection requires exact committed base/head SHAs")
	}
	changed, err := changedFiles(root, base, head)
	if err != nil {
		return err
	}
	current, err := loadImpactManifestAtRevision(root, head)
	if err != nil || current == nil || current.HostedSchemaVersion != 1 {
		return errors.New("head has no supported hosted impact policy")
	}
	baseline, err := loadImpactManifestAtRevision(root, base)
	if err != nil {
		return err
	}
	plan, err := classifyHosted(*current, baseline, changed, full)
	if err != nil {
		return err
	}
	plan.BaseSHA, plan.HeadSHA = base, head
	return json.NewEncoder(out).Encode(plan)
}

func validateHostedPolicy(policy impactManifest) error {
	if policy.HostedSchemaVersion == 0 {
		// A legacy baseline cannot select skips; the planner widens it to full.
		return nil
	}
	if policy.HostedSchemaVersion != 1 || len(policy.Rules) == 0 {
		return errors.New("unsupported or empty hosted impact policy")
	}
	for _, rule := range policy.Rules {
		if len(rule.HostedLanes) == 0 || !slices.IsSorted(rule.HostedLanes) {
			return errors.New("hosted impact rules require sorted nonempty lanes")
		}
		for index, lane := range rule.HostedLanes {
			if !slices.Contains(hosted.Lanes, lane) || index > 0 && lane == rule.HostedLanes[index-1] {
				return errors.New("hosted impact rule has an unknown or repeated lane")
			}
		}
	}
	return nil
}

func classifyHosted(current impactManifest, baseline *impactManifest, changed changedPathSet, full bool) (hosted.Plan, error) {
	if current.HostedSchemaVersion != 1 {
		return hosted.Plan{}, errors.New("current hosted impact policy is required")
	}
	if err := validateImpactStructure(current); err != nil {
		return hosted.Plan{}, err
	}
	if baseline == nil {
		full = true
	} else if err := validateImpactStructure(*baseline); err != nil {
		return hosted.Plan{}, err
	} else if baseline.HostedSchemaVersion == 0 {
		full = true
	}
	selected := map[string]bool{"contracts": true}
	for _, path := range changed.Paths {
		// Policy changes cannot reduce their own verification, even if the new
		// rules reclassify both the policy file and its implementation owners.
		if path == impactManifestPath {
			full = true
		}
		matched := false
		for _, policy := range []*impactManifest{&current, baseline} {
			if policy == nil || policy.HostedSchemaVersion == 0 {
				continue
			}
			for _, rule := range policy.Rules {
				if impactRuleMatches(rule, path) {
					matched = true
					for _, lane := range rule.HostedLanes {
						selected[lane] = true
					}
				}
			}
		}
		if !matched {
			full = true
		}
	}
	// These are semantic lane implications, independent of caller inputs.
	if selected["product"] {
		selected["eval-compat"], selected["security"] = true, true
	}
	if selected["eval-full"] {
		selected["platform"], selected["security"] = true, true
	}
	if selected["platform"] {
		selected["eval-full"], selected["security"] = true, true
	}
	plan := hosted.Plan{SchemaVersion: 1, Full: full}
	for _, lane := range hosted.Lanes {
		if full || selected[lane] {
			plan.Lanes = append(plan.Lanes, lane)
		}
	}
	return plan, nil
}

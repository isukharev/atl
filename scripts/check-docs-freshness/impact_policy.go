package main

import (
	"errors"
	"fmt"
	"strings"
)

// validateImpactStructure is independent of the current tree: historical rules
// may refer to paths and Make targets that no longer exist at the head.
func validateImpactStructure(manifest impactManifest) error {
	if manifest.SchemaVersion != 1 || len(manifest.Checks) == 0 || len(manifest.Rules) == 0 {
		return errors.New("maintainer impact manifest has an invalid version or is empty")
	}
	checks := map[string]bool{}
	previous := ""
	for _, check := range manifest.Checks {
		if !idPattern.MatchString(check.ID) || check.ID <= previous || checks[check.ID] || !idPattern.MatchString(check.MakeTarget) {
			return fmt.Errorf("impact check %q is malformed, duplicated, or unsorted", check.ID)
		}
		previous = check.ID
		checks[check.ID] = true
	}
	previous = ""
	for _, rule := range manifest.Rules {
		key, err := impactRuleKey(rule)
		if err != nil || key <= previous {
			return errors.New("impact rules require valid unique sorted selectors")
		}
		previous = key
		previousExclusion := ""
		for _, exclusion := range rule.ExcludePrefixes {
			if rule.Prefix == "" || exclusion <= previousExclusion ||
				!strings.HasSuffix(exclusion, "/") || exclusion == rule.Prefix ||
				!strings.HasPrefix(exclusion, rule.Prefix) ||
				!canonicalRelative(strings.TrimSuffix(exclusion, "/")) {
				return fmt.Errorf("impact rule %q has a malformed, duplicated, or unsorted excluded prefix", key)
			}
			previousExclusion = exclusion
		}
		if len(rule.Checks) == 0 {
			return fmt.Errorf("impact rule %q has no checks", key)
		}
		priorCheck := ""
		for _, check := range rule.Checks {
			if check <= priorCheck || !checks[check] {
				return fmt.Errorf("impact rule %q has a stale, duplicated, or unsorted check", key)
			}
			priorCheck = check
		}
	}
	return validateHostedPolicy(manifest)
}

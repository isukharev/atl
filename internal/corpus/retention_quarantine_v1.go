package corpus

import (
	"context"
	"os"
	"sort"
	"strings"
)

const (
	retentionQuarantineFamilyPrefix = ".retention-quarantine"
	retentionQuarantinePrefix       = retentionQuarantineFamilyPrefix + ".v1-"
)

type retentionQuarantineSnapshot struct {
	present bool
	ids     []string
}

// requireNoRetentionQuarantine blocks publication, status, and replacement
// plans while an exact prior apply still owns local cleanup. Only replaying the
// original private plan may interpret these names.
func (s *Store) requireNoRetentionQuarantine() error {
	if s == nil || s.root == nil {
		return reject(ReasonType)
	}
	names, err := verifiedDirectoryNames(s.root, generationsDir)
	if err != nil {
		return err
	}
	for _, name := range names {
		_, _, matched, _ := parseRetentionQuarantineName(name)
		if matched {
			return ErrOutcomeUnknown
		}
	}
	return nil
}

func (s *Store) scanRetentionQuarantine(plan RetentionPlanV1) (retentionQuarantineSnapshot, error) {
	var snapshot retentionQuarantineSnapshot
	names, err := verifiedDirectoryNames(s.root, generationsDir)
	if err != nil {
		return snapshot, err
	}
	for _, name := range names {
		planDigest, id, matched, parseErr := parseRetentionQuarantineName(name)
		if !matched {
			continue
		}
		snapshot.present = true
		if parseErr != nil || planDigest != plan.PlanDigest {
			return snapshot, ErrOutcomeUnknown
		}
		path := generationsDir + "/" + name
		entry, statErr := s.root.Lstat(path)
		if statErr != nil || !exactDirectoryMode(entry.Mode()) {
			return snapshot, ErrOutcomeUnknown
		}
		if _, liveErr := s.root.Lstat(generationPath(id)); liveErr == nil || !os.IsNotExist(liveErr) {
			return snapshot, ErrOutcomeUnknown
		}
		snapshot.ids = append(snapshot.ids, id)
	}
	sort.Strings(snapshot.ids)
	return snapshot, nil
}

func validateRetentionQuarantine(plan RetentionPlanV1, inventory retentionInventorySnapshot, quarantine retentionQuarantineSnapshot) error {
	candidates := make(map[string]struct{}, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		candidates[candidate.GenerationID] = struct{}{}
	}
	for _, id := range quarantine.ids {
		if _, allowed := candidates[id]; !allowed {
			return reject(ReasonMembership)
		}
		if _, stillPublished := inventory.entries[id]; stillPublished {
			return reject(ReasonConcurrent)
		}
	}
	return nil
}

// quarantinePinnedRetentionGeneration commits logical deletion with one
// same-parent rename inside generations/. A single parent-directory sync is
// therefore the portable durability barrier. Recursive cleanup begins only
// after the renamed entry is proven to be the already pinned candidate.
func quarantinePinnedRetentionGeneration(s *Store, planDigest string, candidate *retentionPinnedCandidate) error {
	if s == nil || candidate == nil || candidate.generation == nil || !isLowerSHA256(planDigest) {
		return reject(ReasonType)
	}
	id := candidate.record.GenerationID
	if err := s.ensureGenerationRoot(id, candidate.generation.root); err != nil {
		return err
	}
	target := retentionQuarantineGenerationPath(planDigest, id)
	if _, err := s.root.Lstat(target); err == nil {
		return ErrOutcomeUnknown
	} else if !os.IsNotExist(err) {
		return reject(ReasonIO)
	}
	// Close the testable post-namespace-check exchange window immediately
	// before the one name-based operation. The same-authority filesystem owner
	// remains outside the documented physical-immutability boundary.
	if err := s.ensureGenerationRoot(id, candidate.generation.root); err != nil {
		return err
	}
	if err := s.root.Rename(generationPath(id), target); err != nil {
		// A reported local rename failure is deterministic only while the source
		// still names the pinned directory and the destination is absent.
		if sourceErr := s.ensureGenerationRoot(id, candidate.generation.root); sourceErr == nil {
			if _, targetErr := s.root.Lstat(target); os.IsNotExist(targetErr) {
				return reject(ReasonIO)
			}
		}
		return ErrOutcomeUnknown
	}
	if err := verifyQuarantinedGenerationIdentity(s, target, id, candidate.generation.root); err != nil {
		return ErrOutcomeUnknown
	}
	if err := s.hit("after_retention_quarantine_rename"); err != nil {
		return ErrOutcomeUnknown
	}
	if err := syncDirectory(s.root, generationsDir); err != nil {
		return ErrOutcomeUnknown
	}
	if err := s.hit("after_retention_quarantine_sync"); err != nil {
		return ErrOutcomeUnknown
	}
	return nil
}

func verifyQuarantinedGenerationIdentity(s *Store, target, id string, root *os.Root) error {
	if _, err := s.root.Lstat(generationPath(id)); err == nil || !os.IsNotExist(err) {
		return reject(ReasonConcurrent)
	}
	ambient, err := s.root.Lstat(target)
	if err != nil || !exactDirectoryMode(ambient.Mode()) {
		return reject(ReasonConcurrent)
	}
	pinned, err := root.Stat(".")
	if err != nil || !os.SameFile(ambient, pinned) || !exactDirectoryMode(pinned.Mode()) {
		return reject(ReasonConcurrent)
	}
	return nil
}

func (s *Store) purgeRetentionQuarantine(ctx context.Context, plan RetentionPlanV1) error {
	quarantine, err := s.scanRetentionQuarantine(plan)
	if err != nil {
		return err
	}
	if !quarantine.present {
		return nil
	}
	if err := validateRetentionQuarantine(plan, retentionInventorySnapshot{entries: map[string]retentionGenerationEntry{}}, quarantine); err != nil {
		return ErrOutcomeUnknown
	}
	for _, id := range quarantine.ids {
		path := retentionQuarantineGenerationPath(plan.PlanDigest, id)
		quarantineRoot, err := s.root.OpenRoot(path)
		if err != nil {
			return ErrOutcomeUnknown
		}
		ambient, err := s.root.Lstat(path)
		pinned, pinnedErr := quarantineRoot.Stat(".")
		if err != nil || pinnedErr != nil || !os.SameFile(ambient, pinned) || !exactDirectoryMode(pinned.Mode()) {
			_ = quarantineRoot.Close()
			return ErrOutcomeUnknown
		}
		names, err := verifiedDirectoryNames(quarantineRoot, ".")
		if err != nil {
			_ = quarantineRoot.Close()
			return ErrOutcomeUnknown
		}
		for _, name := range names {
			if contextError(ctx) != nil || s.hit("before_retention_quarantine_purge") != nil {
				_ = quarantineRoot.Close()
				return ErrOutcomeUnknown
			}
			if err := quarantineRoot.RemoveAll(name); err != nil {
				_ = quarantineRoot.Close()
				return ErrOutcomeUnknown
			}
			if _, err := quarantineRoot.Lstat(name); err == nil || !os.IsNotExist(err) {
				_ = quarantineRoot.Close()
				return ErrOutcomeUnknown
			}
			if err := syncDirectory(quarantineRoot, "."); err != nil {
				_ = quarantineRoot.Close()
				return ErrOutcomeUnknown
			}
			if err := s.hit("after_retention_quarantine_purge"); err != nil {
				_ = quarantineRoot.Close()
				return ErrOutcomeUnknown
			}
		}
		empty, emptyErr := directoryIsEmpty(quarantineRoot, ".")
		final, finalErr := s.root.Lstat(path)
		if emptyErr != nil || !empty || finalErr != nil || !os.SameFile(final, pinned) || !exactDirectoryMode(final.Mode()) {
			_ = quarantineRoot.Close()
			return ErrOutcomeUnknown
		}
		if err := quarantineRoot.Close(); err != nil {
			return ErrOutcomeUnknown
		}
		final, err = s.root.Lstat(path)
		if err != nil || !os.SameFile(final, pinned) || !exactDirectoryMode(final.Mode()) {
			return ErrOutcomeUnknown
		}
		if err := s.root.Remove(path); err != nil {
			return ErrOutcomeUnknown
		}
		if err := s.hit("after_retention_quarantine_unlink"); err != nil {
			return ErrOutcomeUnknown
		}
		if err := syncDirectory(s.root, generationsDir); err != nil {
			return ErrOutcomeUnknown
		}
	}
	return nil
}

func parseRetentionQuarantineName(name string) (planDigest, generationID string, matched bool, err error) {
	if !strings.HasPrefix(name, retentionQuarantineFamilyPrefix) {
		return "", "", false, nil
	}
	if !strings.HasPrefix(name, retentionQuarantinePrefix) {
		return "", "", true, reject(ReasonFormat)
	}
	rest := strings.TrimPrefix(name, retentionQuarantinePrefix)
	if len(rest) != 64+1+32 || rest[64] != '-' {
		return "", "", true, reject(ReasonFormat)
	}
	planDigest, generationID = rest[:64], rest[65:]
	if !isLowerSHA256(planDigest) || validGenerationID(generationID) != nil {
		return "", "", true, reject(ReasonFormat)
	}
	return planDigest, generationID, true, nil
}

func retentionQuarantineGenerationName(planDigest, generationID string) string {
	return retentionQuarantinePrefix + planDigest + "-" + generationID
}

func retentionQuarantineGenerationPath(planDigest, generationID string) string {
	return generationsDir + "/" + retentionQuarantineGenerationName(planDigest, generationID)
}

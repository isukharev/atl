package corpus

import (
	"os"
	"path/filepath"
	"strings"
)

const rootIdentityDigestDomain = "atl.corpus.root-identity.v1"

type pinnedRootIdentity struct {
	spelling  string
	canonical string
	info      os.FileInfo
	root      *os.Root
}

// RootIdentityDigest returns a content-free binding to one canonical,
// existing owner-only root. Symlink spellings resolve to the same identity;
// the path itself is never returned.
func RootIdentityDigest(rootPath string) (string, error) {
	identity, err := qualifyRootIdentity(rootPath)
	if err != nil {
		return "", err
	}
	digest := domainHash(rootIdentityDigestDomain, []byte(filepath.ToSlash(identity.canonical)))
	if err := identity.revalidate(); err != nil {
		_ = identity.close()
		return "", err
	}
	if err := identity.close(); err != nil {
		return "", err
	}
	return digest, nil
}

// ValidateIndependentRoots proves that two caller-selected owner-only roots
// are neither aliases nor nested trust boundaries. Both identities are pinned
// and revalidated before the relationship is accepted.
func ValidateIndependentRoots(first, second string) error {
	left, err := qualifyRootIdentity(first)
	if err != nil {
		return err
	}
	right, err := qualifyRootIdentity(second)
	if err != nil {
		_ = left.close()
		return err
	}
	invalid := os.SameFile(left.info, right.info) || sameOrNestedPath(left.canonical, right.canonical)
	leftErr := left.revalidate()
	rightErr := right.revalidate()
	leftCloseErr := left.close()
	rightCloseErr := right.close()
	if leftErr != nil || rightErr != nil {
		return reject(ReasonConcurrent)
	}
	if leftCloseErr != nil || rightCloseErr != nil {
		return reject(ReasonIO)
	}
	if invalid {
		return reject(ReasonPath)
	}
	return nil
}

func qualifyRootIdentity(rootPath string) (pinnedRootIdentity, error) {
	if strings.TrimSpace(rootPath) == "" {
		return pinnedRootIdentity{}, reject(ReasonPath)
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		return pinnedRootIdentity{}, reject(ReasonPath)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return pinnedRootIdentity{}, reject(ReasonPath)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return pinnedRootIdentity{}, reject(ReasonPath)
	}
	info, err := os.Lstat(canonical)
	if err != nil {
		return pinnedRootIdentity{}, reject(ReasonIO)
	}
	if !exactDirectoryMode(info.Mode()) {
		return pinnedRootIdentity{}, reject(ReasonMode)
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return pinnedRootIdentity{}, reject(ReasonIO)
	}
	identity := pinnedRootIdentity{spelling: abs, canonical: canonical, info: info, root: root}
	if err := identity.revalidate(); err != nil {
		_ = identity.close()
		return pinnedRootIdentity{}, err
	}
	return identity, nil
}

func (identity *pinnedRootIdentity) revalidate() error {
	if identity == nil || identity.root == nil {
		return reject(ReasonType)
	}
	pinned, statErr := identity.root.Stat(".")
	resolvedAgain, resolveErr := filepath.EvalSymlinks(identity.spelling)
	if resolveErr == nil {
		resolvedAgain, resolveErr = filepath.Abs(resolvedAgain)
	}
	final, finalErr := os.Lstat(identity.canonical)
	if statErr != nil || resolveErr != nil || finalErr != nil || resolvedAgain != identity.canonical ||
		!os.SameFile(identity.info, pinned) || !os.SameFile(identity.info, final) ||
		!exactDirectoryMode(pinned.Mode()) || !exactDirectoryMode(final.Mode()) {
		return reject(ReasonConcurrent)
	}
	return nil
}

func (identity *pinnedRootIdentity) close() error {
	if identity == nil || identity.root == nil {
		return nil
	}
	err := identity.root.Close()
	identity.root = nil
	if err != nil {
		return reject(ReasonIO)
	}
	return nil
}

func sameOrNestedPath(left, right string) bool {
	leftToRight, leftErr := filepath.Rel(left, right)
	rightToLeft, rightErr := filepath.Rel(right, left)
	return leftErr == nil && containedRelativePath(leftToRight) ||
		rightErr == nil && containedRelativePath(rightToLeft)
}

func containedRelativePath(relative string) bool {
	if relative == "." {
		return true
	}
	return relative != "" && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

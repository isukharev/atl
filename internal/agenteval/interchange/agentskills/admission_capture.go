package agentskills

import (
	"io/fs"
	"os"
	"sort"
)

type structuralCaptureHooks struct {
	afterFirstInventory func()
	beforeOpen          func(int, string)
	beforeRead          func(int, string)
}

type structuralCaptureBudget struct {
	remainingEntries int
	remainingBytes   uint64
	maxDepth         uint32
	maxPathBytes     uint32
	maxFileBytes     uint64
}

func newStructuralCaptureBudget(limits StructuralLimits) *structuralCaptureBudget {
	return &structuralCaptureBudget{
		remainingEntries: int(limits.MaxEntries), remainingBytes: limits.MaxTreeBytes,
		maxDepth: limits.MaxDepth, maxPathBytes: limits.MaxPathBytes, maxFileBytes: limits.MaxFileBytes,
	}
}

type structuralRoot interface {
	Inventory(pass int, budget structuralCaptureBudget, hooks structuralCaptureHooks) (capturedTree, error)
	Close() error
}

func captureStructuralTree(rootPath string, namespace string, budget *structuralCaptureBudget,
	hooks structuralCaptureHooks,
) (capturedTree, *structuralSourceRefusal) {
	root, refusal := openStructuralRoot(rootPath)
	if refusal != nil {
		refusal.namespace = namespace
		return capturedTree{}, refusal
	}
	defer func() { _ = root.Close() }()

	first, err := root.Inventory(1, *budget, hooks)
	if err != nil {
		return capturedTree{}, structuralCaptureRefusal(namespace, err, false)
	}
	if hooks.afterFirstInventory != nil {
		hooks.afterFirstInventory()
	}
	second, err := root.Inventory(2, *budget, hooks)
	if err != nil {
		return capturedTree{}, structuralCaptureRefusal(namespace, err, true)
	}
	if !sameCapturedTree(first, second) {
		return capturedTree{}, &structuralSourceRefusal{
			code: FindingTreeChanged, class: FindingSourceInstability,
			namespace: namespace, location: ".",
		}
	}
	budget.remainingEntries -= len(first.entries)
	for _, entry := range first.entries {
		if !entry.isDir {
			budget.remainingBytes -= uint64(len(entry.data))
		}
	}
	return first, nil
}

func structuralCaptureRefusal(namespace string, err error, unstable bool) *structuralSourceRefusal {
	code, location := structuralCaptureError(err)
	class := structuralFindingClass(code)
	if unstable {
		if code != FindingRootChanged {
			code = FindingEntryChanged
		}
		class = FindingSourceInstability
	}
	return &structuralSourceRefusal{
		code: code, class: class, namespace: namespace, location: location,
	}
}

func validateStructuralLocation(location string, budget structuralCaptureBudget) error {
	if structuralPathBytes(location) > budget.maxPathBytes {
		return newStructuralRefusal(FindingPathBytesLimit, ".")
	}
	if !validSourcePath(location) {
		return newStructuralRefusal(FindingInvalidLocation, ".")
	}
	if structuralEntryDepth(location) > budget.maxDepth {
		return newStructuralRefusal(FindingEntryDepthLimit, location)
	}
	return nil
}

func validateStructuralFile(info fs.FileInfo, location string, budget structuralCaptureBudget) error {
	if info == nil {
		return newStructuralRefusal(FindingEntryUnreadable, location)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return newStructuralRefusal(FindingEntrySymlink, location)
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return newStructuralRefusal(FindingSpecialFile, location)
	}
	sizeBytes := uint64(info.Size()) // #nosec G115 -- the negative case is rejected above.
	if sizeBytes > budget.maxFileBytes {
		return newStructuralRefusal(FindingFileBytesLimit, location)
	}
	if sizeBytes > budget.remainingBytes {
		return newStructuralRefusal(FindingTreeBytesLimit, location)
	}
	return nil
}

func addStructuralEntry(tree *capturedTree, entry capturedEntry, budget *structuralCaptureBudget,
	regular []capturedEntry,
) ([]capturedEntry, error) {
	if err := validateStructuralLocation(entry.path, *budget); err != nil {
		return regular, err
	}
	if entry.isDir {
		tree.entries = append(tree.entries, entry)
		return regular, nil
	}
	if err := validateStructuralFile(entry.info, entry.path, *budget); err != nil {
		return regular, err
	}
	for _, previous := range regular {
		if os.SameFile(previous.info, entry.info) {
			return regular, newStructuralRefusal(FindingDuplicateFileIdentity, entry.path)
		}
	}
	tree.entries = append(tree.entries, entry)
	tree.files[entry.path] = entry
	tree.bytes += int64(len(entry.data))
	budget.remainingBytes -= uint64(len(entry.data))
	return append(regular, entry), nil
}

func finishStructuralTree(tree *capturedTree) {
	sort.Slice(tree.entries, func(i, j int) bool { return tree.entries[i].path < tree.entries[j].path })
}

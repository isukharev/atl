package corpus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

type scannedFile struct {
	size   int64
	mode   uint32
	sha256 string
}

type expectedFile struct {
	member *Member
	exact  []byte
	limit  int64
}

func readDirectory(root *os.Root, rel string) ([]os.DirEntry, error) {
	directory, err := root.Open(rel)
	if err != nil {
		return nil, reject(ReasonIO)
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, reject(ReasonIO)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func verifyDirectory(root *os.Root, rel string) error {
	info, err := root.Lstat(rel)
	if err != nil {
		return reject(ReasonIO)
	}
	if !info.IsDir() {
		return reject(ReasonType)
	}
	if info.Mode().Perm() != privateDirMode {
		return reject(ReasonMode)
	}
	opened, err := root.Open(rel)
	if err != nil {
		return reject(ReasonIO)
	}
	defer func() { _ = opened.Close() }()
	pinned, err := opened.Stat()
	if err != nil || !os.SameFile(info, pinned) || !pinned.IsDir() || pinned.Mode().Perm() != privateDirMode {
		return reject(ReasonConcurrent)
	}
	return nil
}

func verifyRegularFile(root *os.Root, rel string, mode os.FileMode) error {
	info, err := root.Lstat(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return os.ErrNotExist
		}
		return reject(ReasonIO)
	}
	if !info.Mode().IsRegular() {
		return reject(ReasonType)
	}
	if info.Mode().Perm() != mode {
		return reject(ReasonMode)
	}
	file, err := root.Open(rel)
	if err != nil {
		return reject(ReasonIO)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != mode {
		return reject(ReasonConcurrent)
	}
	links, err := regularFileLinkCount(file)
	if err != nil {
		return reject(ReasonIO)
	}
	if links != 1 {
		return reject(ReasonType)
	}
	final, err := root.Lstat(rel)
	if err != nil || !os.SameFile(info, final) || !final.Mode().IsRegular() || final.Mode().Perm() != mode {
		return reject(ReasonConcurrent)
	}
	return nil
}

func syncDirectory(root *os.Root, rel string) error {
	directory, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != privateDirMode {
		return reject(ReasonMode)
	}
	return directory.Sync()
}

func ensureArtifactParents(root *os.Root, memberPath string) error {
	parent := path.Dir(memberPath)
	current := artifactsDir
	if err := verifyDirectory(root, current); err != nil {
		return err
	}
	if parent == "." {
		return nil
	}
	for _, segment := range strings.Split(parent, "/") {
		current += "/" + segment
		if err := root.Mkdir(current, privateDirMode); err != nil && !os.IsExist(err) {
			return reject(ReasonIO)
		}
		if err := verifyDirectory(root, current); err != nil {
			return err
		}
	}
	return nil
}

func syncArtifactPath(root *os.Root, memberPath string) error {
	dirs := expectedDirectories([]string{memberPath})
	ordered := make([]string, 0, len(dirs))
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := strings.Count(ordered[i], "/"), strings.Count(ordered[j], "/")
		if left != right {
			return left > right
		}
		return ordered[i] < ordered[j]
	})
	for _, dir := range ordered {
		if err := syncDirectory(root, dir); err != nil {
			return err
		}
	}
	return nil
}

func writeStageMember(ctx context.Context, root *os.Root, memberPath string, reader io.Reader, max int64) (int64, error) {
	return writeExclusiveReader(ctx, root, artifactsDir+"/"+memberPath, reader, max)
}

func writeExclusiveRegular(root *os.Root, rel string, data []byte) error {
	_, err := writeExclusiveReader(context.Background(), root, rel, bytes.NewReader(data), int64(len(data)))
	return err
}

func writeExclusiveReader(ctx context.Context, root *os.Root, rel string, reader io.Reader, max int64) (int64, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	parentRel, base := path.Dir(rel), path.Base(rel)
	parent, err := root.OpenRoot(parentRel)
	if err != nil {
		return 0, reject(ReasonIO)
	}
	defer func() { _ = parent.Close() }()
	if err := verifyDirectory(parent, "."); err != nil {
		return 0, err
	}
	if _, err := parent.Lstat(base); err == nil {
		return 0, ErrAlreadyExists
	} else if !os.IsNotExist(err) {
		return 0, reject(ReasonIO)
	}
	token, err := randomToken(12)
	if err != nil {
		return 0, reject(ReasonIO)
	}
	temp := ".add-" + token
	file, err := parent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return 0, reject(ReasonIO)
	}
	defer func() { _ = parent.Remove(temp) }()
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, reader: reader}, N: max + 1}
	written, copyErr := io.Copy(file, limited)
	if copyErr != nil {
		_ = file.Close()
		if ctx.Err() != nil {
			return 0, contextError(ctx)
		}
		return 0, reject(ReasonIO)
	}
	if written > max {
		_ = file.Close()
		return 0, reject(ReasonBounds)
	}
	if err := file.Chmod(privateFileMode); err != nil {
		_ = file.Close()
		return 0, reject(ReasonIO)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return 0, reject(ReasonIO)
	}
	if err := file.Close(); err != nil {
		return 0, reject(ReasonIO)
	}
	if err := parent.Link(temp, base); err != nil {
		if os.IsExist(err) {
			return 0, ErrAlreadyExists
		}
		return 0, reject(ReasonIO)
	}
	if err := parent.Remove(temp); err != nil {
		return 0, reject(ReasonIO)
	}
	if err := verifyRegularFile(parent, base, privateFileMode); err != nil {
		return 0, err
	}
	return written, nil
}

func scanStage(ctx context.Context, root *os.Root, specs []MemberSpec, manifest []byte, limits Limits) ([]Member, error) {
	ordered := append([]MemberSpec(nil), specs...)
	for _, spec := range ordered {
		if err := validateMemberSpec(spec, limits); err != nil {
			return nil, err
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return lessMemberSpec(ordered[i], ordered[j]) })
	for index := 1; index < len(ordered); index++ {
		previous, current := ordered[index-1], ordered[index]
		if sameMemberTuple(previous, current) {
			return nil, reject(ReasonMembership)
		}
	}
	paths := make(map[string]struct{}, len(ordered))
	folded := make(map[string]struct{}, len(ordered))
	expected := make(map[string]expectedFile, len(ordered)+1)
	memberPaths := make([]string, 0, len(ordered))
	for _, spec := range ordered {
		if _, exists := paths[spec.Path]; exists {
			return nil, reject(ReasonMembership)
		}
		paths[spec.Path] = struct{}{}
		fold := strings.ToLower(spec.Path)
		if _, exists := folded[fold]; exists {
			return nil, reject(ReasonMembership)
		}
		folded[fold] = struct{}{}
		full := artifactsDir + "/" + spec.Path
		expected[full] = expectedFile{limit: limits.MaxMemberBytes}
		memberPaths = append(memberPaths, spec.Path)
	}
	if manifest != nil {
		expected[manifestFile] = expectedFile{exact: manifest, limit: limits.MaxManifestBytes}
	}
	scanned, err := scanExact(ctx, root, expectedDirectories(memberPaths), expected)
	if err != nil {
		return nil, err
	}
	members := make([]Member, 0, len(ordered))
	for _, spec := range ordered {
		actual := scanned[artifactsDir+"/"+spec.Path]
		members = append(members, Member{
			Service: spec.Service, StableID: spec.StableID, Role: spec.Role, Path: spec.Path,
			Size: actual.size, Mode: actual.mode, SHA256: actual.sha256,
		})
	}
	return members, nil
}

func scanSealed(ctx context.Context, root *os.Root, manifest Manifest, manifestBytes, receiptBytes []byte, limits Limits) error {
	expected := make(map[string]expectedFile, len(manifest.Members)+2)
	paths := make([]string, 0, len(manifest.Members))
	for index := range manifest.Members {
		member := manifest.Members[index]
		memberCopy := member
		expected[artifactsDir+"/"+member.Path] = expectedFile{member: &memberCopy, limit: limits.MaxMemberBytes}
		paths = append(paths, member.Path)
	}
	expected[manifestFile] = expectedFile{exact: manifestBytes, limit: limits.MaxManifestBytes}
	expected[receiptFile] = expectedFile{exact: receiptBytes, limit: limits.MaxManifestBytes}
	_, err := scanExact(ctx, root, expectedDirectories(paths), expected)
	return err
}

func scanExact(ctx context.Context, root *os.Root, directories map[string]struct{}, files map[string]expectedFile) (map[string]scannedFile, error) {
	seenDirectories := make(map[string]struct{}, len(directories))
	seenFiles := make(map[string]scannedFile, len(files))
	err := fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return reject(ReasonIO)
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return reject(ReasonType)
		}
		if entry.IsDir() {
			if _, ok := directories[rel]; !ok {
				return reject(ReasonMembership)
			}
			if err := verifyDirectory(root, rel); err != nil {
				return err
			}
			seenDirectories[rel] = struct{}{}
			return nil
		}
		expectation, ok := files[rel]
		if !ok {
			return reject(ReasonMembership)
		}
		var output io.Writer
		if expectation.exact == nil {
			output = io.Discard
		}
		actual, data, err := inspectRegular(ctx, root, rel, expectation.limit, output)
		if err != nil {
			return err
		}
		if expectation.exact != nil && !bytes.Equal(data, expectation.exact) {
			return reject(ReasonDigest)
		}
		if expectation.member != nil {
			member := expectation.member
			if actual.size != member.Size || actual.mode != member.Mode || actual.sha256 != member.SHA256 {
				return reject(ReasonDigest)
			}
		}
		seenFiles[rel] = actual
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrIntegrity) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, reject(ReasonIO)
	}
	if len(seenDirectories) != len(directories) || len(seenFiles) != len(files) {
		return nil, reject(ReasonMembership)
	}
	return seenFiles, nil
}

func expectedDirectories(memberPaths []string) map[string]struct{} {
	directories := map[string]struct{}{".": {}, artifactsDir: {}}
	for _, memberPath := range memberPaths {
		parent := path.Dir(memberPath)
		if parent == "." {
			continue
		}
		current := artifactsDir
		for _, segment := range strings.Split(parent, "/") {
			current += "/" + segment
			directories[current] = struct{}{}
		}
	}
	return directories
}

func inspectRegular(ctx context.Context, root *os.Root, rel string, max int64, output io.Writer) (scannedFile, []byte, error) {
	var zero scannedFile
	info, err := root.Lstat(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, nil, os.ErrNotExist
		}
		return zero, nil, reject(ReasonIO)
	}
	if !info.Mode().IsRegular() {
		return zero, nil, reject(ReasonType)
	}
	if info.Mode().Perm() != privateFileMode {
		return zero, nil, reject(ReasonMode)
	}
	if info.Size() < 0 || info.Size() > max {
		return zero, nil, reject(ReasonBounds)
	}
	file, err := root.Open(rel)
	if err != nil {
		return zero, nil, reject(ReasonIO)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != privateFileMode || opened.Size() != info.Size() {
		return zero, nil, reject(ReasonConcurrent)
	}
	links, err := regularFileLinkCount(file)
	if err != nil {
		return zero, nil, reject(ReasonIO)
	}
	if links != 1 {
		return zero, nil, reject(ReasonType)
	}
	hash := sha256.New()
	var capture bytes.Buffer
	writers := []io.Writer{hash}
	if output != nil {
		writers = append(writers, output)
	} else {
		writers = append(writers, &capture)
	}
	writer := io.MultiWriter(writers...)
	remaining := info.Size()
	buffer := make([]byte, 32*1024)
	for remaining > 0 {
		if err := contextError(ctx); err != nil {
			return zero, nil, err
		}
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		read, readErr := io.ReadFull(file, buffer[:chunk])
		if readErr != nil {
			return zero, nil, reject(ReasonConcurrent)
		}
		if _, writeErr := writer.Write(buffer[:read]); writeErr != nil {
			return zero, nil, reject(ReasonIO)
		}
		remaining -= int64(read)
	}
	var extra [1]byte
	if count, readErr := file.Read(extra[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return zero, nil, reject(ReasonConcurrent)
	}
	finalOpened, err := file.Stat()
	if err != nil || !os.SameFile(info, finalOpened) || finalOpened.Size() != info.Size() || finalOpened.Mode().Perm() != privateFileMode {
		return zero, nil, reject(ReasonConcurrent)
	}
	finalPath, err := root.Lstat(rel)
	if err != nil || !os.SameFile(info, finalPath) || !finalPath.Mode().IsRegular() || finalPath.Size() != info.Size() || finalPath.Mode().Perm() != privateFileMode {
		return zero, nil, reject(ReasonConcurrent)
	}
	actual := scannedFile{size: info.Size(), mode: uint32(info.Mode().Perm()), sha256: hex.EncodeToString(hash.Sum(nil))}
	return actual, capture.Bytes(), nil
}

func readRegularBytes(root *os.Root, rel string, max int64) ([]byte, error) {
	_, data, err := inspectRegular(context.Background(), root, rel, max, nil)
	return data, err
}

func readRequiredRegularBytes(root *os.Root, rel string, max int64) ([]byte, error) {
	data, err := readRegularBytes(root, rel, max)
	if os.IsNotExist(err) {
		return nil, reject(ReasonMembership)
	}
	return data, err
}

func lessMemberSpec(left, right MemberSpec) bool {
	if left.Service != right.Service {
		return left.Service < right.Service
	}
	if left.StableID != right.StableID {
		return left.StableID < right.StableID
	}
	return left.Role < right.Role
}

func sameMemberTuple(left, right MemberSpec) bool {
	return left.Service == right.Service && left.StableID == right.StableID && left.Role == right.Role
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(buffer)
	}
}

// CopyMember streams one tuple through the pinned generation root and validates
// its mode, identity, size, link count, and digest. The destination must discard
// partial output whenever this method returns an error.
func (g *Generation) CopyMember(ctx context.Context, service Service, stableID string, role Role, destination io.Writer) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || destination == nil {
		return 0, reject(ReasonType)
	}
	var selected *Member
	for index := range g.manifest.Members {
		member := &g.manifest.Members[index]
		if member.Service == service && member.StableID == stableID && member.Role == role {
			selected = member
			break
		}
	}
	if selected == nil {
		return 0, reject(ReasonMembership)
	}
	actual, _, err := inspectRegular(ctx, g.root, artifactsDir+"/"+selected.Path, g.limits.MaxMemberBytes, destination)
	if err != nil {
		return 0, err
	}
	if actual.size != selected.Size || actual.mode != selected.Mode || actual.sha256 != selected.SHA256 {
		return 0, reject(ReasonDigest)
	}
	return actual.size, nil
}

//go:build windows

package agenteval

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	extensionWindowsRootAttempts     = 128
	extensionWindowsRandomNameBytes  = 16
	extensionWindowsFileAllAccess    = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	extensionWindowsAddSubdirectory  = 0x00000004
	extensionWindowsDirectoryAccess  = windows.FILE_LIST_DIRECTORY | windows.FILE_TRAVERSE | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL | windows.SYNCHRONIZE
	extensionWindowsRootAccess       = extensionWindowsDirectoryAccess | windows.DELETE
	extensionWindowsExecutableAccess = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE | windows.READ_CONTROL
)

// extensionRuntimeRootGuard keeps both the selected temporary parent and the
// private runtime root open without FILE_SHARE_DELETE. That prevents a
// permissive parent's FILE_DELETE_CHILD right from renaming or deleting the
// runtime root while it is in use.
type extensionRuntimeRootGuard struct {
	base windows.Handle
	root windows.Handle
}

// extensionExecutableLaunchGuard holds the exact executable file open with
// only FILE_SHARE_READ after its ACL, file identity, and digest are admitted.
// The caller must retain it through exec.Cmd.Start.
type extensionExecutableLaunchGuard struct {
	file *os.File
}

// makePrivateExtensionRuntimeRoot creates a directory relative to a held,
// non-reparse temporary base. NtCreateFile applies the protected DACL and
// returns the no-share-delete root handle in the same operation.
func makePrivateExtensionRuntimeRoot(base, prefix string) (string, *extensionRuntimeRootGuard, error) {
	var ok bool
	base, ok = cleanExtensionRuntimeBase(base)
	if !ok || !validExtensionRuntimePrefix(prefix) {
		return "", nil, errExtensionInvalidExecutable
	}
	baseHandle, err := openExtensionWindowsPath(
		base,
		extensionWindowsDirectoryAccess|extensionWindowsAddSubdirectory,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		true,
	)
	if err != nil {
		return "", nil, errExtensionInvalidExecutable
	}
	guard := &extensionRuntimeRootGuard{base: baseHandle, root: windows.InvalidHandle}
	createdPath := ""
	fail := func() (string, *extensionRuntimeRootGuard, error) {
		if createdPath != "" {
			if err := guard.remove(createdPath); err != nil {
				return "", nil, errExtensionAdmissionCleanup
			}
		} else if err := guard.close(); err != nil {
			return "", nil, errExtensionAdmissionCleanup
		}
		return "", nil, errExtensionInvalidExecutable
	}
	canonicalBase, err := extensionWindowsFinalPath(baseHandle)
	if err != nil {
		return fail()
	}

	sd, _, err := extensionWindowsPrivateSecurityDescriptor(true)
	if err != nil {
		return fail()
	}
	for range extensionWindowsRootAttempts {
		suffix := make([]byte, extensionWindowsRandomNameBytes)
		if _, err := rand.Read(suffix); err != nil {
			return fail()
		}
		name := prefix + hex.EncodeToString(suffix)
		objectName, err := windows.NewNTUnicodeString(name)
		if err != nil {
			return fail()
		}
		oa := &windows.OBJECT_ATTRIBUTES{
			Length:             uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
			RootDirectory:      baseHandle,
			ObjectName:         objectName,
			Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
			SecurityDescriptor: sd,
		}
		var status windows.IO_STATUS_BLOCK
		var rootHandle windows.Handle
		err = windows.NtCreateFile(
			&rootHandle,
			extensionWindowsRootAccess,
			oa,
			&status,
			nil,
			windows.FILE_ATTRIBUTE_DIRECTORY,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
			windows.FILE_CREATE,
			windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT,
			0,
			0,
		)
		runtime.KeepAlive(sd)
		runtime.KeepAlive(objectName)
		if err != nil {
			if status, ok := err.(windows.NTStatus); ok && status == windows.STATUS_OBJECT_NAME_COLLISION {
				continue
			}
			return fail()
		}
		guard.root = rootHandle
		rootPath := filepath.Join(canonicalBase, name)
		createdPath = rootPath
		if err := verifyExtensionWindowsHandle(rootHandle, true); err != nil {
			return fail()
		}
		if err := verifyExtensionWindowsFinalPath(rootHandle, rootPath); err != nil {
			return fail()
		}
		return rootPath, guard, nil
	}
	return fail()
}

func preparePrivateExtensionRuntimeDirectory(path string) error {
	if !validExtensionRuntimeBase(path) {
		return errExtensionInvalidExecutable
	}
	handle, err := openExtensionWindowsPath(
		path,
		extensionWindowsDirectoryAccess|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		true,
	)
	if err != nil {
		return errExtensionInvalidExecutable
	}
	prepareErr := applyExtensionWindowsPrivateACL(handle, true)
	if prepareErr == nil {
		prepareErr = verifyExtensionWindowsFinalPath(handle, path)
	}
	closeErr := windows.CloseHandle(handle)
	if prepareErr != nil || closeErr != nil {
		return errExtensionInvalidExecutable
	}
	return nil
}

// preparePrivateExtensionRuntimeExecutable protects and hashes the copied
// executable through the same exact handle retained across Start.
// Omitting FILE_SHARE_WRITE and FILE_SHARE_DELETE makes the admission fail if
// a conflicting writable/deletable handle already exists and blocks later
// replacement, rename, deletion, and data-write opens until close.
func preparePrivateExtensionRuntimeExecutable(path, expectedSHA256 string) (*extensionExecutableLaunchGuard, error) {
	if !validExtensionRuntimeBase(path) || !validSHA256(expectedSHA256) {
		return nil, errExtensionInvalidExecutable
	}
	prepareHandle, err := openExtensionWindowsPath(
		path,
		extensionWindowsExecutableAccess|windows.WRITE_DAC,
		windows.FILE_SHARE_READ,
		false,
	)
	if err != nil {
		return nil, errExtensionInvalidExecutable
	}
	if err := applyExtensionWindowsPrivateACL(prepareHandle, false); err != nil {
		_ = windows.CloseHandle(prepareHandle)
		return nil, errExtensionInvalidExecutable
	}
	file := os.NewFile(uintptr(prepareHandle), path)
	if file == nil {
		_ = windows.CloseHandle(prepareHandle)
		return nil, errExtensionInvalidExecutable
	}
	guard := &extensionExecutableLaunchGuard{file: file}
	fail := func() (*extensionExecutableLaunchGuard, error) {
		_ = guard.close()
		return nil, errExtensionInvalidExecutable
	}
	if err := verifyExtensionWindowsHandle(prepareHandle, false); err != nil {
		return fail()
	}
	if err := verifyExtensionWindowsFinalPath(prepareHandle, path); err != nil {
		return fail()
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > extensionExecutableMaxBytes {
		return fail()
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, extensionExecutableMaxBytes+1))
	if err != nil || written != info.Size() || hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return fail()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail()
	}
	return guard, nil
}

func (g *extensionRuntimeRootGuard) close() error {
	if g == nil {
		return nil
	}
	var errs []error
	if g.root != 0 && g.root != windows.InvalidHandle {
		errs = append(errs, windows.CloseHandle(g.root))
		g.root = windows.InvalidHandle
	}
	if g.base != 0 && g.base != windows.InvalidHandle {
		errs = append(errs, windows.CloseHandle(g.base))
		g.base = windows.InvalidHandle
	}
	return errors.Join(errs...)
}

// remove enumerates the runtime tree through a duplicate of the admitted root
// handle, marks the exact held directory delete-pending, and only then closes
// the root and temporary-parent handles. It never deletes an unanchored root.
func (g *extensionRuntimeRootGuard) remove(rootPath string) error {
	if g == nil || g.root == 0 || g.root == windows.InvalidHandle ||
		g.base == 0 || g.base == windows.InvalidHandle || !validExtensionRuntimeBase(rootPath) {
		return errExtensionAdmissionCleanup
	}
	pathMatches := verifyExtensionWindowsFinalPath(g.root, rootPath) == nil
	if pathMatches {
		entries, err := readExtensionWindowsRuntimeRoot(g.root, rootPath)
		if err != nil {
			pathMatches = false
		}
		for _, entry := range entries {
			name := entry.Name()
			if !filepath.IsLocal(name) || filepath.Base(name) != name || name == "." || name == ".." {
				pathMatches = false
				break
			}
			if err := os.RemoveAll(filepath.Join(rootPath, name)); err != nil {
				pathMatches = false
				break
			}
		}
		if pathMatches {
			remaining, err := readExtensionWindowsRuntimeRoot(g.root, rootPath)
			pathMatches = err == nil && len(remaining) == 0
		}
	}
	deleteFile := byte(1)
	dispositionErr := windows.SetFileInformationByHandle(
		g.root,
		windows.FileDispositionInfo,
		&deleteFile,
		uint32(unsafe.Sizeof(deleteFile)),
	)
	closeErr := g.close()
	if dispositionErr == nil && closeErr == nil && pathMatches {
		if _, err := os.Lstat(rootPath); !os.IsNotExist(err) {
			return errExtensionAdmissionCleanup
		}
	}
	if !pathMatches || dispositionErr != nil || closeErr != nil {
		return errExtensionAdmissionCleanup
	}
	return nil
}

// readExtensionWindowsRuntimeRoot duplicates the already admitted directory
// handle. Opening the path again would conflict with the original handle's
// no-share-delete contract and would also weaken the identity anchor.
func readExtensionWindowsRuntimeRoot(root windows.Handle, rootPath string) ([]os.DirEntry, error) {
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(
		windows.CurrentProcess(),
		root,
		windows.CurrentProcess(),
		&duplicate,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), rootPath)
	if file == nil {
		_ = windows.CloseHandle(duplicate)
		return nil, errExtensionAdmissionCleanup
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	return entries, errors.Join(readErr, closeErr)
}

func (g *extensionExecutableLaunchGuard) close() error {
	if g == nil || g.file == nil {
		return nil
	}
	err := g.file.Close()
	g.file = nil
	return err
}

func extensionPlatformEnvironment() (map[string]string, error) {
	directory, err := windows.GetWindowsDirectory()
	if err != nil || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || strings.IndexByte(directory, 0) >= 0 {
		return nil, errExtensionInvalidExecutable
	}
	return map[string]string{
		"SYSTEMROOT": directory,
		"WINDIR":     directory,
	}, nil
}

func applyExtensionWindowsPrivateACL(handle windows.Handle, directory bool) error {
	sd, _, err := extensionWindowsPrivateSecurityDescriptor(directory)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errExtensionInvalidExecutable
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return err
	}
	runtime.KeepAlive(sd)
	return verifyExtensionWindowsHandle(handle, directory)
}

func extensionWindowsPrivateSecurityDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, nil, errExtensionInvalidExecutable
	}
	sid := user.User.Sid
	flags := ""
	if directory {
		flags = "OICI"
	}
	sddl := "O:" + sid.String() + "D:P(A;" + flags + ";FA;;;" + sid.String() + ")"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil || sd == nil || !sd.IsValid() {
		return nil, nil, errExtensionInvalidExecutable
	}
	return sd, sid, nil
}

func verifyExtensionWindowsHandle(handle windows.Handle, directory bool) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	isDirectory := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errExtensionInvalidExecutable
	}
	sd, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || sd == nil || !sd.IsValid() {
		return errExtensionInvalidExecutable
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errExtensionInvalidExecutable
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return errExtensionInvalidExecutable
	}
	_, current, err := extensionWindowsPrivateSecurityDescriptor(directory)
	if err != nil || !owner.Equals(current) {
		return errExtensionInvalidExecutable
	}
	dacl, defaulted, err := sd.DACL()
	if err != nil || dacl == nil || defaulted || dacl.AceCount != 1 {
		return errExtensionInvalidExecutable
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return errExtensionInvalidExecutable
	}
	wantFlags := uint8(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	if ace.Header.AceFlags != wantFlags || ace.Mask != extensionWindowsFileAllAccess {
		return errExtensionInvalidExecutable
	}
	aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !aceSID.IsValid() || !aceSID.Equals(current) {
		return errExtensionInvalidExecutable
	}
	return nil
}

func openExtensionWindowsPath(path string, access, share uint32, directory bool) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(name, access, share, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return windows.InvalidHandle, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, err
	}
	isDirectory := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDirectory != directory || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, errExtensionInvalidExecutable
	}
	return handle, nil
}

func verifyExtensionWindowsFinalPath(handle windows.Handle, expected string) error {
	actual, err := extensionWindowsFinalPath(handle)
	if err != nil {
		return err
	}
	want, wantOK := normalizeExtensionWindowsPath(expected)
	if !wantOK || !strings.EqualFold(actual, want) {
		return errExtensionInvalidExecutable
	}
	return nil
}

func extensionWindowsFinalPath(handle windows.Handle) (string, error) {
	size := uint32(512)
	for range 4 {
		buffer := make([]uint16, size)
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], size, 0)
		if err != nil {
			return "", err
		}
		if length < size {
			actual, ok := normalizeExtensionWindowsPath(windows.UTF16ToString(buffer[:length]))
			if !ok {
				return "", errExtensionInvalidExecutable
			}
			return actual, nil
		}
		if length > 32768 {
			return "", errExtensionInvalidExecutable
		}
		size = length + 1
	}
	return "", errExtensionInvalidExecutable
}

func normalizeExtensionWindowsPath(path string) (string, bool) {
	switch {
	case strings.HasPrefix(path, `\\?\UNC\`):
		path = `\\` + strings.TrimPrefix(path, `\\?\UNC\`)
	case strings.HasPrefix(path, `\\?\`):
		path = strings.TrimPrefix(path, `\\?\`)
	}
	if !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return "", false
	}
	return filepath.Clean(path), true
}

func validExtensionRuntimeBase(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && strings.IndexByte(path, 0) < 0
}

func cleanExtensionRuntimeBase(path string) (string, bool) {
	if path == "" || !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return "", false
	}
	return filepath.Clean(path), true
}

func validExtensionRuntimePrefix(prefix string) bool {
	return prefix != "" && len(prefix) <= 128 && filepath.Base(prefix) == prefix &&
		!strings.ContainsAny(prefix, `/\\`) && strings.IndexByte(prefix, 0) < 0
}

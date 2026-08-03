//go:build windows

package safepath

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openFileNoFollow(path string, perm os.FileMode) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	attributes := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if perm.Perm()&0o200 == 0 {
		attributes = windows.FILE_ATTRIBUTE_READONLY | windows.FILE_FLAG_OPEN_REPARSE_POINT
	}
	const shareMode = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE

	// FILE_FLAG_OPEN_REPARSE_POINT cannot be combined with CREATE_ALWAYS.
	// Open an existing path without following it, and use CREATE_NEW only when
	// the path was absent. If another process wins that creation race,
	// CREATE_NEW fails rather than reopening and potentially following its path.
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_WRITE,
		shareMode,
		nil, // nil security attributes make the handle non-inheritable.
		windows.OPEN_EXISTING,
		attributes,
		0,
	)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		handle, err = windows.CreateFile(
			pathPointer,
			windows.GENERIC_WRITE,
			shareMode,
			nil,
			windows.CREATE_NEW,
			attributes,
			0,
		)
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		return newFileFromWindowsHandle(handle, path)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{
			Op:   "open",
			Path: path,
			Err:  errors.New("final path component is a reparse point"),
		}
	}

	// Inspection and truncation use the same handle, so replacing the directory
	// entry after CreateFile cannot redirect the write to another object.
	if _, err := windows.SetFilePointer(handle, 0, nil, windows.FILE_BEGIN); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "truncate", Path: path, Err: err}
	}
	if err := windows.SetEndOfFile(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "truncate", Path: path, Err: err}
	}
	return newFileFromWindowsHandle(handle, path)
}

func newFileFromWindowsHandle(handle windows.Handle, path string) (*os.File, error) {
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("invalid Windows file handle")}
	}
	return file, nil
}

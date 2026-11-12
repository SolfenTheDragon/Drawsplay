//go:build windows

package main

// Result files written by elevated helpers for a non-elevated reader.
//
// The helper task (-winusb-bind-attached) and the elevated install and
// uninstall phases run as administrator but report back through files in
// folders the user can write to (%APPDATA%\DrawSplay, %TEMP%). A program
// running as that user without elevation could replace such a folder with
// a junction (or plant a symlink under the result's name) so that the
// administrator's write lands somewhere protected. writeElevatedResult
// therefore opens the destination folder, confirms that the path the
// kernel actually resolved is the folder that was asked for, and then
// creates and renames the file relative to that open folder handle, never
// following reparse points on the final component.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// writeElevatedResult writes v as indented JSON to path (see the file
// comment for why this is not writeJSONAtomic).
func writeElevatedResult(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileInVerifiedDir(path, append(data, '\n'))
}

// splitElevatedResultPath validates path as a plain absolute local file
// path and returns its folder and file name.
func splitElevatedResultPath(path string) (dir, name string, err error) {
	clean := filepath.Clean(path)
	vol := filepath.VolumeName(clean)
	if !filepath.IsAbs(clean) || len(vol) != 2 || vol[1] != ':' || strings.HasPrefix(clean, `\\`) {
		return "", "", fmt.Errorf("result path %q must be an absolute local path", path)
	}
	dir, name = filepath.Dir(clean), filepath.Base(clean)
	if name == "." || name == ".." || name == "" || strings.ContainsAny(name, `\/`) || dir == clean {
		return "", "", fmt.Errorf("result path %q has no file name", path)
	}
	return dir, name, nil
}

func writeFileInVerifiedDir(path string, data []byte) error {
	dir, name, err := splitElevatedResultPath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dh, err := openVerifiedDirectory(dir)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(dh)

	tmpName := name + ".tmp-" + rand.Text()
	fh, err := createFileRelative(dh, tmpName)
	if err != nil {
		return fmt.Errorf("create %s in %s: %w", tmpName, dir, err)
	}
	f := os.NewFile(uintptr(fh), filepath.Join(dir, tmpName))
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = renameRelative(windows.Handle(f.Fd()), dh, name)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = deleteRelative(dh, tmpName)
	}
	return err
}

// openVerifiedDirectory opens dir and checks that the kernel's final path
// for the opened folder is dir itself: a junction or symlink anywhere in the
// path would resolve elsewhere and is refused.
func openVerifiedDirectory(dir string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", dir, err)
	}
	final, err := finalPathByHandle(h)
	if err != nil {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("resolve %s: %w", dir, err)
	}
	want := longPathName(dir)
	if !strings.EqualFold(filepath.Clean(final), filepath.Clean(want)) {
		windows.CloseHandle(h)
		return 0, fmt.Errorf("refusing to write into %s: it resolves to %s (reparse point)", dir, final)
	}
	return h, nil
}

// finalPathByHandle returns the normalized DOS path of an open file without
// the \\?\ prefix; UNC and other non-drive paths are rejected.
func finalPathByHandle(h windows.Handle) (string, error) {
	// GetFinalPathNameByHandle flags: FILE_NAME_NORMALIZED | VOLUME_NAME_DOS.
	const normalizedDOSPath = 0x0
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), normalizedDOSPath)
		if err != nil {
			return "", err
		}
		if int(n) < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]uint16, n+1)
	}
	s := windows.UTF16ToString(buf)
	s = strings.TrimPrefix(s, `\\?\`)
	if strings.HasPrefix(strings.ToUpper(s), `UNC\`) || strings.HasPrefix(s, `\\`) {
		return "", errors.New("not a local drive path")
	}
	return s, nil
}

// longPathName expands 8.3 short names (common in %TEMP%) so the path can
// be compared with the kernel's normalized final path. On failure the path
// is returned unchanged and the comparison then fails closed.
func longPathName(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetLongPathName(p, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return path
		}
		if int(n) < len(buf) {
			return windows.UTF16ToString(buf[:n])
		}
		buf = make([]uint16, n+1)
	}
}

// createFileRelative creates a new file named name directly inside the open
// folder dir. Creation fails if name exists, and reparse points are opened
// rather than followed, so a planted link cannot redirect the write.
func createFileRelative(dir windows.Handle, name string) (windows.Handle, error) {
	objName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{
		RootDirectory: dir,
		ObjectName:    objName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.FILE_GENERIC_WRITE|windows.DELETE|windows.SYNCHRONIZE, &oa, &iosb, nil,
		windows.FILE_ATTRIBUTE_NORMAL, 0, windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return 0, ntError(err)
	}
	return h, nil
}

// fileRenameInformation mirrors FILE_RENAME_INFORMATION (ntifs.h); the
// BOOLEAN is padded so RootDirectory lands at its native alignment.
type fileRenameInformation struct {
	ReplaceIfExists bool
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [windows.MAX_PATH]uint16
}

// renameRelative gives the open file f the name newName inside the open
// folder dir, replacing an existing entry of that name.
func renameRelative(f, dir windows.Handle, newName string) error {
	name16, err := windows.UTF16FromString(newName)
	if err != nil {
		return err
	}
	info := fileRenameInformation{ReplaceIfExists: true, RootDirectory: dir}
	if len(name16) > len(info.FileName) {
		return syscall.EINVAL
	}
	copy(info.FileName[:], name16)
	info.FileNameLength = uint32((len(name16) - 1) * 2)
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtSetInformationFile(f, &iosb, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), windows.FileRenameInformation)
	if err != nil {
		return fmt.Errorf("rename to %s: %w", newName, ntError(err))
	}
	return nil
}

// deleteRelative removes name from the open folder dir (best effort).
func deleteRelative(dir windows.Handle, name string) error {
	objName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: dir, ObjectName: objName, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.DELETE|windows.SYNCHRONIZE, &oa, &iosb, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_DELETE_ON_CLOSE, 0, 0)
	if err != nil {
		return ntError(err)
	}
	return windows.CloseHandle(h)
}

func ntError(err error) error {
	if st, ok := err.(windows.NTStatus); ok {
		return st.Errno()
	}
	return err
}

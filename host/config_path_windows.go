//go:build windows

package main

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	driveRemote     = 4
	fileDeleteChild = 0x40 // FILE_DELETE_CHILD
)

var (
	kernel32ConfigPath = windows.NewLazySystemDLL("kernel32.dll")
	procGetDriveTypeW  = kernel32ConfigPath.NewProc("GetDriveTypeW")
)

func isNetworkExecutablePath(path string) bool {
	volume := filepath.VolumeName(path)
	if volume == "" {
		return true
	}
	root := volume + `\`
	rootp, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return true
	}
	driveType, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(rootp)))
	return driveType == driveRemote
}

func validateLocalExecutable(path string, _ fs.FileInfo) error {
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		return errors.New("must name a local .exe file")
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		if err := validateExecutablePathACL(current); err != nil {
			return err
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

func validateExecutablePathACL(path string) error {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("reparse-point executable paths are not allowed")
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	me, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	trustedInstaller, err := windows.StringToSid("S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464")
	if err != nil {
		return err
	}
	if owner == nil ||
		(!windows.EqualSid(owner, me.User.Sid) &&
			!owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) &&
			!owner.IsWellKnown(windows.WinLocalSystemSid) &&
			!windows.EqualSid(owner, trustedInstaller)) {
		return errors.New("executable path must be owned by this user or a trusted system principal")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return errors.New("executable path has no restrictive access control list")
	}
	// For the file itself any write right lets another user change what runs.
	// For an ancestor folder only rights that replace or remove an existing
	// child matter: adding a new file or subfolder (which the default C:\ ACL
	// grants Authenticated Users) cannot swap out a path component.
	const (
		fileWriteAccess = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA |
			windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER |
			windows.GENERIC_WRITE | windows.GENERIC_ALL
		dirWriteAccess = fileDeleteChild | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER |
			windows.GENERIC_WRITE | windows.GENERIC_ALL
	)
	writeAccess := windows.ACCESS_MASK(fileWriteAccess)
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		writeAccess = windows.ACCESS_MASK(dirWriteAccess)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&writeAccess == 0 ||
			ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if windows.EqualSid(sid, me.User.Sid) ||
			sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
			sid.IsWellKnown(windows.WinLocalSystemSid) {
			continue
		}
		return errors.New("executable path is writable by a group or other user")
	}
	return nil
}

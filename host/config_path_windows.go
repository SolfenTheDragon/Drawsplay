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

const driveRemote = 4

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
	const writeAccess = 0x00000002 | 0x00000004 | 0x00000010 | 0x00000100 |
		0x00010000 | 0x00040000 | 0x00080000 | 0x10000000 | 0x40000000
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&writeAccess == 0 {
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

//go:build windows

package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const driverStageSDDL = "O:BAG:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

func driverStageDir() (string, error) {
	if !isProcessElevated() {
		return "", errors.New("driver staging requires administrator approval")
	}
	parent, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", fmt.Errorf("resolve protected Program Files directory: %w", err)
	}
	if parent == "" {
		return "", errors.New("machine Program Files directory is unavailable; refusing driver staging")
	}
	sd, err := windows.SecurityDescriptorFromString(driverStageSDDL)
	if err != nil {
		return "", fmt.Errorf("create driver staging security descriptor: %w", err)
	}
	// Set owner and protected DACL at creation, not afterwards: even the
	// installing user's filtered token must not be able to replace this ACL.
	dir := filepath.Join(parent, "DrawSplay-driver-"+rand.Text())
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", err
	}
	sa := windows.SecurityAttributes{
		Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd,
	}
	if err := windows.CreateDirectory(path, &sa); err != nil {
		return "", fmt.Errorf("create protected driver staging directory: %w", err)
	}
	if err := verifyDriverStageDir(dir); err != nil {
		return "", errors.Join(err, os.Remove(dir))
	}
	return dir, nil
}

func verifyDriverStageDir(dir string) error {
	reject := func(err error) error { return fmt.Errorf("driver signing requires protected staging: %w", err) }
	parent, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return reject(err)
	}
	if !filepath.IsAbs(dir) || !strings.EqualFold(filepath.Dir(filepath.Clean(dir)), filepath.Clean(parent)) {
		return reject(errors.New("directory must be directly under machine Program Files"))
	}
	ptr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return reject(err)
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return reject(err)
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return reject(errors.New("directory cannot be a reparse point or regular file"))
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return reject(err)
	}
	if err := validateDriverStageSecurity(sd); err != nil {
		return reject(err)
	}
	return nil
}

func validateDriverStageSecurity(sd *windows.SECURITY_DESCRIPTOR) error {
	if sd == nil {
		return errors.New("missing security descriptor")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || (!owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) && !owner.IsWellKnown(windows.WinLocalSystemSid)) {
		return errors.New("directory owner must be Administrators or SYSTEM")
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("directory DACL must disable inherited access")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil || acl.AceCount != 2 {
		return errors.New("directory must grant access only to Administrators and SYSTEM")
	}
	var admin, system bool
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE ||
			ace.Mask != windows.STANDARD_RIGHTS_REQUIRED|windows.SYNCHRONIZE|0x1ff {
			return errors.New("directory grants unexpected access or child inheritance")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.IsWellKnown(windows.WinBuiltinAdministratorsSid):
			admin = true
		case sid.IsWellKnown(windows.WinLocalSystemSid):
			system = true
		default:
			return errors.New("directory grants access to an unprivileged principal")
		}
	}
	if !admin || !system {
		return errors.New("directory must grant Administrators and SYSTEM access")
	}
	return nil
}

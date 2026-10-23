package wifilan

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func secureStorePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return errors.New("Wi-Fi trust path must not be a link or device")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := security.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !ownedByCurrentProcess(owner, user.User.Sid) {
		return errors.New("Wi-Fi trust must be owned by the current Windows user; run the host without elevation")
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"D:P(A;%s;FA;;;SY)(A;%s;FA;;;%s)", flags, flags, user.User.Sid.String()))
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// ownedByCurrentProcess accepts the user's own SID or the token's default
// owner: files an elevated administrator creates are owned by the
// Administrators group, not the user, while a non-elevated token's default
// owner is the user itself.
func ownedByCurrentProcess(owner, user *windows.SID) bool {
	if owner.Equals(user) {
		return true
	}
	defaultOwner, err := tokenDefaultOwner(windows.GetCurrentProcessToken())
	return err == nil && owner.Equals(defaultOwner)
}

func tokenDefaultOwner(token windows.Token) (*windows.SID, error) {
	size := uint32(64)
	for {
		buffer := make([]byte, size)
		err := windows.GetTokenInformation(token, windows.TokenOwner, &buffer[0], size, &size)
		if err == nil {
			// TOKEN_OWNER is a single PSID pointing into buffer.
			return (*struct{ Owner *windows.SID })(unsafe.Pointer(&buffer[0])).Owner.Copy()
		}
		if err != windows.ERROR_INSUFFICIENT_BUFFER {
			return nil, err
		}
	}
}

func lockTrustFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped))
}

func protectTrustKey(data []byte) ([]byte, error)   { return cryptTrustKey(data, false) }
func unprotectTrustKey(data []byte) ([]byte, error) { return cryptTrustKey(data, true) }

func cryptTrustKey(data []byte, decrypt bool) (_ []byte, result error) {
	if len(data) == 0 || len(data) > 8192 {
		return nil, errors.New("invalid Wi-Fi protected key size")
	}
	entropy := []byte("DrawSplay LAN identity v1")
	input := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	scope := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var output windows.DataBlob
	var err error
	if decrypt {
		err = windows.CryptUnprotectData(&input, nil, &scope, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		err = windows.CryptProtectData(&input, nil, &scope, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	runtime.KeepAlive(data)
	runtime.KeepAlive(entropy)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, freeError := windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
		result = errors.Join(result, freeError)
	}()
	if output.Size == 0 || output.Size > 8192 || output.Data == nil {
		return nil, errors.New("DPAPI returned an invalid Wi-Fi key size")
	}
	raw := unsafe.Slice(output.Data, int(output.Size))
	defer clear(raw)
	return append([]byte{}, raw...), nil
}

// Windows flushes the completed file before replacement; it has no equivalent
// of the Linux directory fsync used here.
func syncTrustDirectory(*os.Root) error { return nil }

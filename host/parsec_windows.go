//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Parsec's legacy VDD protocol (github.com/nomi-san/parsec-vdd,
// core/parsec-vdd.h) uses 32-byte input, overlapped IOCTLs and a 50 ms
// keepalive. REMOVE encodes its display index in big-endian order.
const (
	parsecIOCTLAdd    = 0x0022e004
	parsecIOCTLRemove = 0x0022a008
	parsecIOCTLUpdate = 0x0022a00c
)

var parsecInterfaceGUID = windows.GUID{
	Data1: 0x00b41627, Data2: 0x04c4, Data3: 0x429e,
	Data4: [8]byte{0xa2, 0x6e, 0x02, 0x65, 0xcf, 0x50, 0xc8, 0xfa},
}

type parsecDevice struct {
	handle windows.Handle
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
}

func parsecDevicePath() (string, error) {
	var size uint32
	ret, _, _ := procCMGetDevIfaceListSizeW.Call(
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&parsecInterfaceGUID)),
		0, cmGetDeviceInterfaceListPresent)
	if ret != 0 || size <= 1 {
		return "", errors.New("Parsec VDD device interface not found")
	}
	buf := make([]uint16, size)
	ret, _, _ = procCMGetDeviceInterfaceListW.Call(
		uintptr(unsafe.Pointer(&parsecInterfaceGUID)), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(size), cmGetDeviceInterfaceListPresent)
	if ret != 0 {
		return "", fmt.Errorf("enumerate Parsec VDD interfaces: %d", ret)
	}
	path := windows.UTF16ToString(buf)
	if path == "" {
		return "", errors.New("Parsec VDD device interface list empty")
	}
	return path, nil
}

func openParsecDevice() (*parsecDevice, error) {
	path, err := parsecDevicePath()
	if err != nil {
		return nil, err
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(ptr, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_NO_BUFFERING|windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		return nil, fmt.Errorf("open Parsec VDD: %w", err)
	}
	d := &parsecDevice{handle: handle, stop: make(chan struct{}), done: make(chan struct{})}
	go d.keepalive()
	return d, nil
}

func (d *parsecDevice) keepalive() {
	defer close(d.done)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			if _, err := d.ioctl(parsecIOCTLUpdate, [32]byte{}); err != nil {
				log.Printf("Parsec VDD keepalive failed: %v", err)
				return
			}
		}
	}
}

func (d *parsecDevice) AddMonitor(_, _, _, _, _ int) (int, error) {
	index, err := d.ioctl(parsecIOCTLAdd, [32]byte{})
	if err != nil {
		return -1, fmt.Errorf("Parsec VDD add display: %w", err)
	}
	if _, err := d.ioctl(parsecIOCTLUpdate, [32]byte{}); err != nil {
		if removeErr := d.RemoveMonitor(int(index)); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
		return -1, fmt.Errorf("Parsec VDD initial keepalive: %w", err)
	}
	return int(index), nil
}

func parsecRemovePayload(index int) ([32]byte, error) {
	var payload [32]byte
	if index < 0 || index > 65535 {
		return payload, fmt.Errorf("invalid Parsec display index %d", index)
	}
	binary.BigEndian.PutUint16(payload[:2], uint16(index))
	return payload, nil
}

func (d *parsecDevice) RemoveMonitor(index int) error {
	payload, err := parsecRemovePayload(index)
	if err != nil {
		return err
	}
	if _, err := d.ioctl(parsecIOCTLRemove, payload); err != nil {
		return fmt.Errorf("Parsec VDD remove display %d: %w", index, err)
	}
	return nil
}

func (d *parsecDevice) Close() {
	d.once.Do(func() {
		close(d.stop)
		<-d.done
		if err := windows.CloseHandle(d.handle); err != nil {
			log.Printf("Close Parsec VDD device: %v", err)
		}
	})
}

func (d *parsecDevice) ioctl(code uint32, input [32]byte) (uint32, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	var output, returned uint32
	overlapped := windows.Overlapped{HEvent: event}
	err = windows.DeviceIoControl(d.handle, code, &input[0], uint32(len(input)),
		(*byte)(unsafe.Pointer(&output)), uint32(unsafe.Sizeof(output)), &returned, &overlapped)
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) {
		return 0, err
	}
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		wait, waitErr := windows.WaitForSingleObject(event, 5000)
		if waitErr != nil || wait != windows.WAIT_OBJECT_0 {
			// Do not release stack buffers or the event until cancellation has
			// completed; the driver can still be writing to them after timeout.
			cancelErr := windows.CancelIoEx(d.handle, &overlapped)
			resultErr := windows.GetOverlappedResult(d.handle, &overlapped, &returned, true)
			if errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
				cancelErr = nil
			}
			if errors.Is(resultErr, windows.ERROR_OPERATION_ABORTED) {
				resultErr = nil
			}
			if waitErr == nil {
				waitErr = fmt.Errorf("Parsec IOCTL wait returned %d: %w", wait, windows.ERROR_TIMEOUT)
			}
			return 0, errors.Join(waitErr, cancelErr, resultErr)
		}
		if err := windows.GetOverlappedResult(d.handle, &overlapped, &returned, false); err != nil {
			return 0, err
		}
	}
	// UPDATE and REMOVE complete without a result body on Parsec 0.41.
	if code == parsecIOCTLAdd && returned < uint32(unsafe.Sizeof(output)) {
		return 0, fmt.Errorf("Parsec IOCTL short response: %d bytes", returned)
	}
	return output, nil
}

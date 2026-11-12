package main

// The DrawSplay IddCx driver's control interface (drivers/windows/idd/Public.h).
// dd-host speaks it too (desktop/host/src/idd_protocol.rs); both are tested
// against testdata/protocols/idd-ioctl.vectors. It is plain data, so it builds
// and is tested on every platform.

import "encoding/binary"

// iddInterfaceGUIDString is the device interface GUID of the control device.
const iddInterfaceGUIDString = "4f73af95-3814-4206-a651-125dfbb5e8f5"

// CTL_CODE(FILE_DEVICE_UNKNOWN, 0x800+n, METHOD_BUFFERED, FILE_ANY_ACCESS).
const (
	iddIoctlAddMonitor    = 0x00222000
	iddIoctlRemoveMonitor = 0x00222004
	iddIoctlRemoveAll     = 0x00222008
)

// encodeIDDAddMonitor is the add-monitor input: five little-endian uint32s,
// width, height, refresh rate, native width and native height (0 = unknown).
func encodeIDDAddMonitor(width, height, refreshHz, nativeWidth, nativeHeight int) [20]byte {
	var in [20]byte
	binary.LittleEndian.PutUint32(in[0:], uint32(width))
	binary.LittleEndian.PutUint32(in[4:], uint32(height))
	binary.LittleEndian.PutUint32(in[8:], uint32(refreshHz))
	binary.LittleEndian.PutUint32(in[12:], uint32(max(nativeWidth, 0)))
	binary.LittleEndian.PutUint32(in[16:], uint32(max(nativeHeight, 0)))
	return in
}

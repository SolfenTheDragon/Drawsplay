package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Android Open Accessory (AOA) host-side protocol constants. See
// https://source.android.com/docs/core/interaction/accessories/aoa
const (
	aoaRequestGetProtocol byte = 51
	aoaRequestSendString  byte = 52
	aoaRequestStart       byte = 53

	aoaReqTypeIn  byte = 0xC0 // vendor, device-to-host, device recipient
	aoaReqTypeOut byte = 0x40 // vendor, host-to-device, device recipient

	aoaStringManufacturer = 0
	aoaStringModel        = 1
	aoaStringDescription  = 2
	aoaStringVersion      = 3
	aoaStringURI          = 4
	aoaStringSerial       = 5

	aoaGoogleVID uint16 = 0x18d1
	aoaPIDFirst  uint16 = 0x2d00
	aoaPIDLast   uint16 = 0x2d05

	aoaControlTimeout = 2 * time.Second
)

// Identification strings. They must match android/app/src/main/res/xml/accessory_filter.xml.
// The version string is deliberately not sent so the Android filter matches on manufacturer and model only.
const (
	aoaManufacturer = "DrawSplay"
	aoaModel        = "Tablet"
	// aoaModelManual deliberately does not match accessory_filter.xml, so
	// Android does not offer to open the app when the tablet is attached.
	aoaModelManual = "TabletManual"
	aoaDescription = "DrawSplay tablet display"
	aoaURI         = "https://github.com/SolfenTheDragon/Drawsplay"
	aoaSerial      = "drawsplay-aoa"
)

var errAOANotSupported = errors.New("device does not support Android Open Accessory")

// aoaDeviceHandle is an opened USB device. Implementations are per-OS.
type aoaDeviceHandle interface {
	// Control performs a control transfer on endpoint 0 and returns the byte count transferred.
	Control(reqType, request byte, value, index uint16, data []byte) (int, error)
	// OpenPipe claims the accessory interface and returns a bulk byte pipe.
	OpenPipe() (aoaPipe, error)
	Close() error
}

// aoaPipe is the bulk IN/OUT byte channel of an accessory-mode device.
// Write must terminate transfers that are an exact multiple of the endpoint
// packet size with a zero-length packet so the Android side does not stall.
type aoaPipe interface {
	io.Reader
	io.Writer
	io.Closer
}

// aoaCandidate describes one attached USB device.
type aoaCandidate struct {
	VID, PID uint16
	Key      string // stable per-attachment identifier (sysfs name / device path)
	// FileTransfer is set when the device exposes an MTP/PTP interface, i.e. the
	// user chose "File transfer" on the tablet; it must not be switched away.
	FileTransfer bool
	Open         func() (aoaDeviceHandle, error)
}

func (c aoaCandidate) inAccessoryMode() bool {
	return c.VID == aoaGoogleVID && c.PID >= aoaPIDFirst && c.PID <= aoaPIDLast
}

// aoaKnownVendors lists USB vendor IDs of common Android tablet makers. Only
// these (plus DRAWSPLAY_AOA_VIDS) are probed so unrelated USB devices never
// receive vendor control requests.
var aoaKnownVendors = map[uint16]string{
	0x18d1: "Google", 0x04e8: "Samsung", 0x12d1: "Huawei", 0x2717: "Xiaomi",
	0x17ef: "Lenovo", 0x0b05: "ASUS", 0x1004: "LG", 0x054c: "Sony",
	0x0bb4: "HTC", 0x22b8: "Motorola", 0x2a70: "OnePlus", 0x22d9: "OPPO",
	0x1949: "Amazon", 0x0502: "Acer", 0x0955: "NVIDIA", 0x2d95: "vivo",
	0x19d2: "ZTE", 0x1bbb: "Alcatel/TCL", 0x0fce: "Sony Mobile", 0x413c: "Dell",
	0x2a45: "Meizu", 0x1782: "Spreadtrum", 0x2207: "Rockchip", 0x1f3a: "Allwinner",
	0x0e8d: "MediaTek", 0x05c6: "Qualcomm", 0x109b: "Hisense", 0x0489: "Foxconn",
	0x2916: "Yota", 0x1d4d: "Pegatron", 0x2b0e: "Fairphone", 0x0421: "Nokia",
}

// aoaExtraVendorsEnv holds comma-separated hex vendor IDs to probe in addition to aoaKnownVendors.
const aoaExtraVendorsEnv = "DRAWSPLAY_AOA_VIDS"

func aoaVendorProbeAllowed(vid uint16) bool {
	if _, ok := aoaKnownVendors[vid]; ok {
		return true
	}
	for _, field := range strings.Split(os.Getenv(aoaExtraVendorsEnv), ",") {
		field = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(field)), "0x"), "0X")
		if field == "" {
			continue
		}
		if v, err := strconv.ParseUint(field, 16, 16); err == nil && uint16(v) == vid {
			return true
		}
	}
	return false
}

// aoaSwitchToAccessory asks an attached Android device to re-enumerate in
// accessory mode. The device disappears and returns as 18d1:2d00-2d05.
// autoLaunch selects the model string that lets Android offer to launch the app.
func aoaSwitchToAccessory(dev aoaDeviceHandle, autoLaunch bool) error {
	model := aoaModel
	if !autoLaunch {
		model = aoaModelManual
	}
	var proto [2]byte
	n, err := dev.Control(aoaReqTypeIn, aoaRequestGetProtocol, 0, 0, proto[:])
	if err != nil {
		return fmt.Errorf("get protocol: %w", err)
	}
	if n < 2 || (uint16(proto[0])|uint16(proto[1])<<8) < 1 {
		return errAOANotSupported
	}
	strs := []struct {
		index uint16
		value string
	}{
		{aoaStringManufacturer, aoaManufacturer},
		{aoaStringModel, model},
		{aoaStringDescription, aoaDescription},
		{aoaStringURI, aoaURI},
		{aoaStringSerial, aoaSerial},
	}
	for _, s := range strs {
		data := append([]byte(s.value), 0)
		if _, err := dev.Control(aoaReqTypeOut, aoaRequestSendString, 0, s.index, data); err != nil {
			return fmt.Errorf("send string %d: %w", s.index, err)
		}
	}
	if _, err := dev.Control(aoaReqTypeOut, aoaRequestStart, 0, 0, nil); err != nil {
		return fmt.Errorf("start accessory mode: %w", err)
	}
	return nil
}

// aoaInterface is the bulk interface chosen from a configuration descriptor.
type aoaInterface struct {
	Number             int
	EpIn, EpOut        byte
	MaxPacketOut       int
	MaxPacketIn        int
	ConfigurationValue int
}

// parseAOAInterface walks raw USB descriptors (device + configuration, as
// exposed by usbfs or GET_DESCRIPTOR) and selects the accessory interface: the
// first vendor-specific interface with one bulk IN and one bulk OUT endpoint
// that is not the ADB interface (class 0xff, subclass 0x42, protocol 0x01).
func parseAOAInterface(desc []byte) (aoaInterface, error) {
	var (
		cur      *aoaInterface
		curValid bool
		cfgValue int
		best     *aoaInterface
	)
	flush := func() {
		if best == nil && cur != nil && curValid && cur.EpIn != 0 && cur.EpOut != 0 {
			c := *cur
			best = &c
		}
	}
	for off := 0; off+2 <= len(desc); {
		l := int(desc[off])
		if l < 2 || off+l > len(desc) {
			break
		}
		typ := desc[off+1]
		switch typ {
		case 2: // configuration
			if l >= 6 {
				cfgValue = int(desc[off+5])
			}
		case 4: // interface
			flush()
			if l >= 9 {
				class, sub, proto := desc[off+5], desc[off+6], desc[off+7]
				cur = &aoaInterface{Number: int(desc[off+2]), ConfigurationValue: cfgValue}
				curValid = class == 0xff && !(sub == 0x42 && proto == 0x01)
			}
		case 5: // endpoint
			if cur != nil && l >= 7 {
				addr, attrs := desc[off+2], desc[off+3]
				mps := int(desc[off+4]) | int(desc[off+5])<<8
				if attrs&0x03 == 0x02 { // bulk
					if addr&0x80 != 0 && cur.EpIn == 0 {
						cur.EpIn, cur.MaxPacketIn = addr, mps&0x7ff
					} else if addr&0x80 == 0 && cur.EpOut == 0 {
						cur.EpOut, cur.MaxPacketOut = addr, mps&0x7ff
					}
				}
			}
		}
		off += l
	}
	flush()
	if best == nil {
		return aoaInterface{}, errors.New("no accessory bulk interface found in descriptors")
	}
	return *best, nil
}

// aoaWriteNeedsZLP reports whether a bulk write of n bytes must be followed by a zero-length packet.
func aoaWriteNeedsZLP(n, maxPacket int) bool {
	return n > 0 && maxPacket > 0 && n%maxPacket == 0
}

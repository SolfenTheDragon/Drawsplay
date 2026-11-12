package main

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// decodeINFText handles UTF-16LE INFs (with BOM) as well as ANSI/UTF-8.
func decodeINFText(data []byte) string {
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		u := make([]uint16, 0, len(data)/2)
		for i := 2; i+1 < len(data); i += 2 {
			u = append(u, uint16(data[i])|uint16(data[i+1])<<8)
		}
		return string(utf16.Decode(u))
	}
	return string(data)
}

type infDriverVersion struct {
	date    time.Time
	version [4]uint16
}

func parseINFDriverVersion(data []byte) (infDriverVersion, bool) {
	for _, line := range strings.Split(decodeINFText(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "DriverVer") {
			continue
		}
		value, _, _ = strings.Cut(value, ";")
		date, version, ok := strings.Cut(value, ",")
		if !ok {
			return infDriverVersion{}, false
		}
		stamp, err := time.Parse("01/02/2006", strings.TrimSpace(date))
		parts := strings.Split(strings.TrimSpace(version), ".")
		if err != nil || len(parts) != 4 {
			return infDriverVersion{}, false
		}
		result := infDriverVersion{date: stamp}
		for i, part := range parts {
			n, err := strconv.ParseUint(part, 10, 16)
			if err != nil {
				return infDriverVersion{}, false
			}
			result.version[i] = uint16(n)
		}
		return result, true
	}
	return infDriverVersion{}, false
}

func driverPackageNeedsUpdate(bundledINF, bundledDLL, installedINF, installedDLL []byte) bool {
	bundled, bundleOK := parseINFDriverVersion(bundledINF)
	installed, installedOK := parseINFDriverVersion(installedINF)
	if bundleOK && installedOK {
		// Windows ranks matching packages by date, then version.
		if !bundled.date.Equal(installed.date) {
			return bundled.date.After(installed.date)
		}
		if bundled.version != installed.version {
			for i, part := range bundled.version {
				if part != installed.version[i] {
					return part > installed.version[i]
				}
			}
		}
	}
	normalize := func(data []byte) string {
		return strings.TrimSpace(strings.ReplaceAll(decodeINFText(data), "\r\n", "\n"))
	}
	return normalize(bundledINF) != normalize(installedINF) || !bytes.Equal(bundledDLL, installedDLL)
}

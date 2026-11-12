package main

import "testing"

func TestDriverPackageNeedsUpdate(t *testing.T) {
	current := []byte("[Version]\r\nDriverVer=11/11/2026,1.2.3.0\r\n")
	dll := []byte("current driver")
	tests := []struct {
		name string
		inf  []byte
		dll  []byte
		want bool
	}{
		{"same package", current, dll, false},
		{"older version", []byte("DriverVer=11/11/2026,1.2.2.0"), dll, true},
		{"older date", []byte("DriverVer=11/10/2026,9.0.0.0"), dll, true},
		{"newer version", []byte("DriverVer=11/11/2026,1.2.4.0"), []byte("newer"), false},
		{"newer date", []byte("DriverVer=11/12/2026,1.0.0.0"), []byte("newer"), false},
		{"rebuilt binary", current, []byte("old driver"), true},
		{"changed INF", append(append([]byte{}, current...), []byte("PnpLockDown=1")...), dll, true},
		{"different line endings", []byte("[Version]\nDriverVer=11/11/2026,1.2.3.0\n"), dll, false},
		{"missing package", nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := driverPackageNeedsUpdate(current, dll, tt.inf, tt.dll); got != tt.want {
				t.Fatalf("needs update = %v, want %v", got, tt.want)
			}
		})
	}
}

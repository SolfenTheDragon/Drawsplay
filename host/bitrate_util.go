package main

import "fmt"

func cappedBitrate(current, capValue string) string {
	cur := parseBitrateKbps(current)
	capKbps := parseBitrateKbps(capValue)
	if capKbps <= 0 || cur <= capKbps {
		return current
	}
	return fmt.Sprintf("%dk", capKbps)
}

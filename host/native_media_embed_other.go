//go:build !windows

package main

// platformNativeHelperPath: only Windows builds embed the helper; Linux
// installs it next to the host binary (scripts/build-native-media.sh).
func platformNativeHelperPath() string { return "" }

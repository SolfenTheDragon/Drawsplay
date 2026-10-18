package main

import "runtime/debug"

// version is set at release build time with
// -ldflags "-X main.version=v1.2.3"; local builds report "dev" plus the short
// commit when the build embeds VCS information.
var version = "dev"

func hostVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return version + "-" + s.Value[:7]
		}
	}
	return version
}

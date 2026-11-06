package main

import "flag"

// bindSettingFlags declares the command-line flags that override saved
// settings, each bound to its field in s. It is the only list of them: main
// binds it once to read the command line (defaults from defaultSettings) and
// once to apply the flags given to the loaded settings.
func bindSettingFlags(fs *flag.FlagSet, s *settings) {
	fs.StringVar(&s.ADB, "adb", s.ADB, "ADB executable path")
	fs.StringVar(&s.PreferredADBSerial, "adb-serial", s.PreferredADBSerial, "preferred ADB device serial (optional; reverse only this device)")
	fs.BoolVar(&s.RequirePreferredADB, "require-adb-serial", s.RequirePreferredADB, "require preferred ADB serial and refuse unapproved devices")
	fs.BoolVar(&s.AutoLaunchAndroidApp, "auto-launch-android-app", s.AutoLaunchAndroidApp, "launch the DrawSplay Android app automatically on eligible tablet connect")
	fs.BoolVar(&s.ZeroCopyCapture, "zero-copy-capture", s.ZeroCopyCapture, "try zero-copy NVENC capture path first on Windows (falls back automatically)")
	fs.BoolVar(&s.TemporalLayering, "temporal-layering", s.TemporalLayering, "enable temporal enhancement-layer probe and runtime non-reference frame adaptation when supported")
	fs.BoolVar(&s.AOAEnabled, "aoa-enabled", s.AOAEnabled, "ADB-free USB transport over Android Open Accessory (no developer mode needed)")
	fs.BoolVar(&s.AOAAutoSwitch, "aoa-auto-switch", s.AOAAutoSwitch, "automatically switch an attached tablet to accessory mode (otherwise use the web UI button)")
	fs.BoolVar(&s.ADBTunnelEnabled, "adb-tunnel", s.ADBTunnelEnabled, "enable the adb reverse tunnel transport (needs USB debugging on the tablet)")
	fs.StringVar(&s.FFmpeg, "ffmpeg", s.FFmpeg, "ffmpeg executable path (used for screen capture/encode)")
	fs.BoolVar(&s.AllowToolAutoInstall, "allow-tool-auto-install", s.AllowToolAutoInstall, "allow automatic download of the newest ffmpeg when missing (Windows)")
	fs.IntVar(&s.FPS, "fps", s.FPS, "capture frame rate")
	fs.StringVar(&s.Bitrate, "bitrate", s.Bitrate, "video bitrate at 1080p (ffmpeg syntax, e.g. 24M); scaled by display pixel count and capped at the USB 2.0 link budget")
	fs.IntVar(&s.Width, "width", s.Width, "virtual display width (extend mode)")
	fs.IntVar(&s.Height, "height", s.Height, "virtual display height (extend mode)")
	fs.BoolVar(&s.NoDriverInstall, "no-driver-install", s.NoDriverInstall, "skip automatic virtual display driver installation (Windows)")
	fs.BoolVar(&s.KeepAliveOnBackground, "keep-alive-on-background", s.KeepAliveOnBackground, "keep tablet session alive while the Android app is backgrounded")
}

// applySettingFlags copies every setting flag that was given on the command
// line (set) onto cfg. Flags left out keep the saved value.
func applySettingFlags(set *flag.FlagSet, cfg *settings) {
	target := flag.NewFlagSet("settings", flag.ContinueOnError)
	bindSettingFlags(target, cfg)
	set.Visit(func(f *flag.Flag) {
		if target.Lookup(f.Name) != nil {
			// Same flag type on both sets, so the value always parses.
			_ = target.Set(f.Name, f.Value.String())
		}
	})
}

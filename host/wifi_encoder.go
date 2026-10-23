package main

// wifiH264Level is the level requested from encoders on the paired Wi-Fi path;
// it matches the H264Level the host offers during negotiation.
const wifiH264Level = "5.0"

// wifiProfileArgs are the settings appended after the normal encoder args for
// the paired Wi-Fi path: H.264 constrained baseline, a single slice per
// picture and a level within the negotiated one. It returns nil for non-H.264
// encoders. B-frames are already disabled by encoderArgs.
func wifiProfileArgs(encoder, level string) []string {
	switch encoder {
	case "libx264":
		// Later -x264-params wins over the base args; -profile baseline makes
		// x264 emit constrained baseline (constraint_set1).
		return []string{"-profile:v", "baseline", "-level", level, "-x264-params", "sliced-threads=0:slices=1:aud=0"}
	case "h264_nvenc":
		return []string{"-profile:v", "baseline", "-level", level}
	case "h264_amf":
		return []string{"-profile:v", "constrained_baseline", "-level", level}
	case "h264_qsv":
		return []string{"-profile:v", "baseline", "-level", level, "-slices", "1"}
	default:
		return nil
	}
}

// wifiEncoderArgs returns the full encoder settings for the paired Wi-Fi path,
// or nil for encoders that cannot produce the required stream.
func wifiEncoderArgs(encoder string, fps int, level string) []string {
	profile := wifiProfileArgs(encoder, level)
	if profile == nil {
		return nil
	}
	return append(encoderArgs(encoder, fps), profile...)
}

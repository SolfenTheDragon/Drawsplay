package main

// wifiEncoderArgs returns encoder settings for the paired Wi-Fi path: H.264
// constrained baseline, no B-frames, a single slice per picture and a level
// within what the tablet negotiated. It returns nil for non-H.264 encoders.
// The shared USB encoderArgs are untouched.
func wifiEncoderArgs(encoder string, fps int, level string) []string {
	args := encoderArgs(encoder, fps)
	switch encoder {
	case "libx264":
		// encoderArgs already disables sliced threads; slices=1 makes the
		// single-slice contract explicit. -profile baseline sets
		// constraint_set1 in x264 (constrained baseline).
		return append(args, "-profile:v", "baseline", "-level", level, "-x264-params", "sliced-threads=0:slices=1:aud=0")
	case "h264_nvenc":
		return append(args, "-profile:v", "baseline", "-level", level)
	case "h264_amf":
		return append(args, "-profile:v", "constrained_baseline", "-level", level)
	case "h264_qsv":
		return append(args, "-profile:v", "baseline", "-level", level, "-slices", "1")
	default:
		return nil
	}
}

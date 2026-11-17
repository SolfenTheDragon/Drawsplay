//go:build windows

package main

func platformSetupFix(action string, cfg settings) (msg string, handled bool, err error) {
	switch action {
	case setupFixFFmpeg:
		if _, err := installBundledTool("ffmpeg"); err != nil {
			return "", true, err
		}
		return "ffmpeg is downloaded.", true, nil
	}
	return "", false, nil
}

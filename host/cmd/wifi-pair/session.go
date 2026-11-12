package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"drawsplay/host/internal/wifilan"
)

// verifySession connects with the saved trust exactly as the host does
// (wifilan.ConnectPairedHost), then optionally streams a synthetic video.
// Trust comes from the paired identity, never from discovery metadata.
func verifySession(ctx context.Context, iface, controlEndpoint, videoEndpoint string, store *wifilan.TrustStore, testVideo string, forceRecovery bool, out io.Writer) (result error) {
	s, err := wifilan.ConnectPairedHost(ctx, iface, controlEndpoint, videoEndpoint, store)
	if err != nil {
		return fmt.Errorf("saved trust could not establish a fresh paired session: %w", err)
	}
	defer func() { result = errors.Join(result, s.Close()) }()
	if _, err = fmt.Fprintln(out, "Paired control/video channels verified using saved trust. No capture or input was started."); err != nil {
		return err
	}
	if testVideo != "" {
		return streamSyntheticVideo(ctx, testVideo, s.Control, s.Video, s.Binding, s.Limits, forceRecovery, out)
	}
	return nil
}

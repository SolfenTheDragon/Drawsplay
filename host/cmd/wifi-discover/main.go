package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"drawsplay/host/internal/wifilan"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("wifi-discover", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	iface := flags.String("interface", "", "explicit LAN interface to browse (required)")
	duration := flags.Duration("duration", 15*time.Second, "bounded discovery window (1s..60s)")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(diagnostics, "Wi-Fi discovery: unexpected positional arguments; select a LAN interface, not a hostname")
		return 1
	}
	if *duration < time.Second || *duration > time.Minute {
		fmt.Fprintln(diagnostics, "Wi-Fi discovery: duration must be 1s..60s")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()
	seen := make(map[string]bool)
	rejected := 0
	var outputErr error
	err := wifilan.Browse(ctx, *iface, func(candidate wifilan.Candidate) {
		data, err := json.Marshal(candidate)
		if err != nil {
			outputErr = err
			cancel()
			return
		}
		key := string(data)
		if seen[key] {
			return
		}
		if len(seen) >= 64 {
			outputErr = fmt.Errorf("candidate limit reached (64); refusing further discovery")
			cancel()
			return
		}
		seen[key] = true
		if _, err := fmt.Fprintln(out, key); err != nil {
			outputErr = err
			cancel()
		}
	}, func(err error) {
		rejected++
		if rejected <= 5 {
			fmt.Fprintf(diagnostics, "Ignored untrusted tablet advertisement: %v\n", err)
		}
	})
	if outputErr != nil {
		fmt.Fprintf(diagnostics, "Wi-Fi discovery: %v\n", outputErr)
		return 1
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(diagnostics, "Wi-Fi discovery: %v\n", err)
		return 1
	}
	fmt.Fprintf(diagnostics, "Discovery finished: %d untrusted candidates; %d rejected advertisements. No connections or trust changes made.\n", len(seen), rejected)
	if len(seen) == 0 {
		fmt.Fprintln(diagnostics, "No tablets found. Check tablet Wi-Fi availability mode and the selected LAN. Multicast filtering, guest isolation or a firewall may prevent discovery; a timeout cannot distinguish them.")
		return 2
	}
	return 0
}

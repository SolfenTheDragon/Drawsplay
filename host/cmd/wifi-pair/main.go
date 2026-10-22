// Command wifi-pair is an opt-in enrollment preview, independent of the running
// USB host. It discovers tablets and saves trust; it never captures or injects.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"drawsplay/host/internal/wifilan"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(arguments []string, approval io.Reader, out, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("wifi-pair", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	iface := flags.String("interface", "", "explicit directly connected LAN interface (required)")
	tablet := flags.String("tablet", "", "exact discovered tablet service name (required)")
	directory := flags.String("trust-dir", "", "dedicated private Wi-Fi identity/trust directory (required)")
	replace := flags.Bool("replace", false, "explicitly permit replacement of an existing paired tablet after approval")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if *iface == "" || *directory == "" || len(*tablet) == 0 || len(*tablet) > 63 || !utf8.ValidString(*tablet) ||
		strings.IndexFunc(*tablet, unicode.IsControl) >= 0 || flags.NArg() != 0 {
		fmt.Fprintln(diagnostics, "Wi-Fi pairing requires -interface, -tablet (printable exact service name), and -trust-dir; no positional arguments")
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := pair(ctx, *iface, *tablet, *directory, *replace, approval, out, diagnostics); err != nil {
		fmt.Fprintf(diagnostics, "Wi-Fi pairing: %v\n", err)
		return 1
	}
	return 0
}

func pair(ctx context.Context, iface, name, directory string, replace bool, approval io.Reader, out, diagnostics io.Writer) (result error) {
	if _, _, err := wifilan.SelectInterface(iface); err != nil {
		return err
	}
	fmt.Fprintf(diagnostics, "Looking for %q on %s (10 seconds); open Wi-Fi pairing on the tablet.\n", name, iface)
	discovery, stop := context.WithTimeout(ctx, 10*time.Second)
	candidates := make(map[string]bool)
	rejected := 0
	err := wifilan.Browse(discovery, iface, func(candidate wifilan.Candidate) {
		if candidate.Name != name {
			return
		}
		for _, address := range candidate.Addresses {
			if len(candidates) >= 64 {
				break
			}
			candidates[net.JoinHostPort(address.String(), fmt.Sprint(candidate.InputPort))] = true
		}
	}, func(err error) {
		rejected++
		if rejected <= 5 {
			fmt.Fprintf(diagnostics, "Ignored untrusted advertisement: %v\n", err)
		}
	})
	stop()
	if rejected > 0 {
		fmt.Fprintf(diagnostics, "Discovery rejected %d untrusted advertisements.\n", rejected)
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if len(candidates) != 1 {
		return fmt.Errorf("found %d matching endpoints; require exactly one. Check selected LAN, tablet availability and duplicate names; guest isolation, multicast filtering or a firewall may block discovery", len(candidates))
	}
	var endpoint string
	for address := range candidates {
		endpoint = address
	}
	store, err := wifilan.OpenTrustStore(directory)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.Close()) }()
	if _, paired := store.Peer(); paired && !replace {
		return errors.New("this store already has a paired tablet; use -replace only if you intend to replace that trust")
	}
	invitation, err := wifilan.NewPairingInvitation(time.Now())
	if err != nil {
		return err
	}
	defer invitation.Close()
	if _, err := fmt.Fprintf(out, "Pairing code: %s\nEnter it on the selected tablet, then approve this host there. No screen/input access is enabled.\n", invitation.Code()); err != nil {
		return err
	}
	enrollment, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	raw, err := wifilan.DialSelectedLAN(enrollment, iface, endpoint)
	if err != nil {
		return err
	}
	secured, pin, err := wifilan.AuthenticateEnrollmentHost(enrollment, raw, store.Identity(), invitation)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, secured.Close()) }()
	if _, err := fmt.Fprintf(out, "Code and both TLS identities verified. Approve pairing with %q (%x)? Type yes: ", name, pin[:6]); err != nil {
		return err
	}
	type decision struct {
		text string
		err  error
	}
	answer := make(chan decision, 1)
	go func() {
		text, err := bufio.NewReader(io.LimitReader(approval, 16)).ReadString('\n')
		answer <- decision{text, err}
	}()
	select {
	case <-enrollment.Done():
		return enrollment.Err()
	case selected := <-answer:
		if selected.err != nil || strings.TrimSpace(selected.text) != "yes" {
			return errors.New("pairing not approved locally; no host trust was changed")
		}
	}
	deadline, _ := enrollment.Deadline()
	if err := secured.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err := secured.Write([]byte{1}); err != nil {
		return err
	}
	var acknowledgement [1]byte
	if _, err := io.ReadFull(secured, acknowledgement[:]); err != nil || acknowledgement[0] != 1 {
		return fmt.Errorf("tablet did not confirm saved trust: %w", errors.Join(err, wifilan.ErrPairing))
	}
	if err := store.Pair(pin, name); err != nil {
		return fmt.Errorf("tablet saved this host but host trust could not be saved; repair locally and pair again: %w", err)
	}
	if _, err := secured.Write([]byte{2}); err != nil {
		return fmt.Errorf("host trust saved, but tablet did not receive final confirmation; verify trust locally: %w", err)
	}
	_, err = fmt.Fprintln(out, "Pairing saved on both devices. This preview does not yet stream video or inject input.")
	return err
}

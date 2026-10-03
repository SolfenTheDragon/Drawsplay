package main

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"strconv"
	"strings"
)

// startUSBLinkTransport serves a directly reachable computer client over a
// USB virtual NIC. It deliberately has no dependency on ADB or Wi-Fi discovery.
func startUSBLinkTransport(ctx context.Context, store *settingsStore) {
	cfg := store.get()
	if !cfg.USBLinkEnabled {
		return
	}
	if strings.TrimSpace(cfg.USBLinkAuthToken) == "" {
		log.Printf("USB Link disabled: an auth token is required")
		return
	}

	inputAddr := net.JoinHostPort(cfg.USBLinkBindAddress, strconv.Itoa(cfg.USBLinkInputPort))
	videoAddr := net.JoinHostPort(cfg.USBLinkBindAddress, strconv.Itoa(cfg.USBLinkVideoPort))

	var (
		listenFn    func(string) (net.Listener, error)
		fingerprint string
	)
	if cfg.USBLinkTLS {
		cert, fp, err := ensureWiFiTLSCertificate()
		if err != nil {
			log.Printf("USB Link TLS setup failed; USB Link transport disabled: %v", err)
			return
		}
		fingerprint = fp
		tlsConfig := &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
		listenFn = func(addr string) (net.Listener, error) {
			return tls.Listen("tcp", addr, tlsConfig)
		}
	}

	var (
		inputLn net.Listener
		err     error
	)
	if listenFn != nil {
		inputLn, err = listenFn(inputAddr)
	} else {
		inputLn, err = net.Listen("tcp", inputAddr)
	}
	if err != nil {
		log.Printf("USB Link input listener disabled: %v", err)
		return
	}
	log.Printf("USB Link input listener on %s (tls=%v)", inputAddr, cfg.USBLinkTLS)
	if fingerprint != "" {
		log.Printf("USB Link TLS certificate fingerprint (SHA-256): %s", fingerprint)
	}
	go func() {
		<-ctx.Done()
		_ = inputLn.Close()
	}()
	go runInputListener(ctx, inputLn, store, "usb-link")

	go runVideoListenerAt(ctx, store, videoAddr, "usb-link", listenFn)
	log.Printf("USB Link video listener on %s (tls=%v)", videoAddr, cfg.USBLinkTLS)
}

func transportAuthToken(cfg settings, transport string) (string, bool) {
	switch transport {
	case "wifi":
		return strings.TrimSpace(cfg.WifiAuthToken), true
	case "usb-link":
		return strings.TrimSpace(cfg.USBLinkAuthToken), true
	default:
		return "", false
	}
}

func transportMaxBitrate(cfg settings, transport string) string {
	switch transport {
	case "wifi":
		return cfg.WifiMaxBitrate
	case "usb-link":
		return cfg.USBLinkMaxBitrate
	default:
		return ""
	}
}

func transportDisplayName(transport string) string {
	switch transport {
	case "wifi":
		return "Wi-Fi"
	case "usb-link":
		return "USB Link"
	default:
		return transport
	}
}

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

var wifiPolicyState struct {
	sync.RWMutex
	tlsRequired bool
	tlsFP       string
}

func publishWiFiPolicyTLS(required bool, fingerprint string) {
	wifiPolicyState.Lock()
	wifiPolicyState.tlsRequired = required
	wifiPolicyState.tlsFP = fingerprint
	wifiPolicyState.Unlock()
}

func currentWiFiPolicyTLS() (bool, string) {
	wifiPolicyState.RLock()
	required := wifiPolicyState.tlsRequired
	fp := wifiPolicyState.tlsFP
	wifiPolicyState.RUnlock()
	return required, fp
}

func startWiFiTransport(ctx context.Context, store *settingsStore) {
	cfg := store.get()
	publishWiFiPolicyTLS(false, "")
	if !wifiFeatureEnabled() {
		return
	}
	if !cfg.WifiEnabled {
		return
	}

	inputAddr := net.JoinHostPort(cfg.WifiBindAddress, strconv.Itoa(cfg.WifiInputPort))
	videoAddr := net.JoinHostPort(cfg.WifiBindAddress, strconv.Itoa(cfg.WifiVideoPort))

	var (
		fingerprint string
		listenFn    func(string) (net.Listener, error)
	)
	if cfg.WifiTLS {
		cert, fp, err := ensureWiFiTLSCertificate()
		if err != nil {
			log.Printf("Wi-Fi TLS setup failed; Wi-Fi transport disabled: %v", err)
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
		publishWiFiPolicyTLS(true, fingerprint)
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
		log.Printf("Wi-Fi input listener disabled: %v", err)
		return
	}
	log.Printf("Wi-Fi input listener on %s", inputAddr)
	go func() {
		<-ctx.Done()
		_ = inputLn.Close()
	}()
	go runInputListener(ctx, inputLn, store, "wifi")

	go runVideoListenerAt(ctx, store, videoAddr, "wifi", listenFn)
	log.Printf("Wi-Fi video listener on %s (tls=%v)", videoAddr, cfg.WifiTLS)

	svc, err := registerWiFiService(cfg, fingerprint)
	if err != nil {
		log.Printf("Wi-Fi discovery advertisement disabled: %v", err)
		return
	}
	log.Printf("Wi-Fi discovery advertised as %q (_drawsplay._tcp)", cfg.WifiServiceName)
	go func() {
		<-ctx.Done()
		svc.Shutdown()
	}()
}

func registerWiFiService(cfg settings, tlsFingerprint string) (*zeroconf.Server, error) {
	txt := []string{
		"v=1",
		"videoPort=" + strconv.Itoa(cfg.WifiVideoPort),
	}
	if cfg.WifiTLS {
		txt = append(txt, "tls=1", "tlsFp="+tlsFingerprint)
	} else {
		txt = append(txt, "tls=0")
	}
	if cfg.WifiAuthToken != "" {
		txt = append(txt, "auth=1")
	} else {
		txt = append(txt, "auth=0")
	}
	return zeroconf.Register(cfg.WifiServiceName, "_drawsplay._tcp", "local.", cfg.WifiInputPort, txt, nil)
}

func ensureWiFiTLSCertificate() (tls.Certificate, string, error) {
	dir, err := configDir()
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certPath := filepath.Join(dir, "wifi_tls_cert.pem")
	keyPath := filepath.Join(dir, "wifi_tls_key.pem")
	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			cert, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err != nil {
				return tls.Certificate{}, "", err
			}
			if len(cert.Certificate) > 0 {
				parsed, err := x509.ParseCertificate(cert.Certificate[0])
				if err == nil && parsed.PublicKeyAlgorithm != x509.Ed25519 {
					fp, err := certificateFingerprint(cert)
					return cert, fp, err
				}
				_ = os.Remove(certPath)
				_ = os.Remove(keyPath)
			}
		}
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "DrawSplay Wi-Fi",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(5 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"drawsplay.local"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, priv.Public(), priv)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certPem := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDer, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDer})
	if err := os.WriteFile(certPath, certPem, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(keyPath, keyPem, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(certPem, keyPem)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	fp, err := certificateFingerprint(cert)
	return cert, fp, err
}

func certificateFingerprint(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", fmt.Errorf("certificate chain is empty")
	}
	sum := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(sum[:]), nil
}

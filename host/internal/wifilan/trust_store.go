package wifilan

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const trustSizeLimit = 16384

type trustRecord struct {
	Version int    `json:"version"`
	Cert    []byte `json:"certificate"`
	Key     []byte `json:"protectedKey"`
	Peer    string `json:"tabletSpki,omitempty"`
	Label   string `json:"tabletLabel,omitempty"`
}

// TrustStore is single-owner. Pair is called only after PAKE, mutual TLS and
// explicit local confirmation. Revocation callers must also close live sessions.
type TrustStore struct {
	root     *os.Root
	lock     *os.File
	record   trustRecord
	identity tls.Certificate
	closed   bool
}

func OpenTrustStore(directory string) (_ *TrustStore, result error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create Wi-Fi trust directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("Wi-Fi trust path must be a real private directory: %w", errors.Join(err, os.ErrPermission))
	}
	if err := secureStorePath(directory, true); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	store := &TrustStore{root: root}
	defer func() {
		if result != nil {
			result = errors.Join(result, store.Close())
		}
	}()
	lockStatus := store.checkFile("trust.lock")
	firstInitialization := errors.Is(lockStatus, os.ErrNotExist)
	if lockStatus != nil && !firstInitialization {
		return nil, lockStatus
	}
	store.lock, err = root.OpenFile("trust.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := secureStorePath(filepath.Join(directory, "trust.lock"), false); err != nil {
		return nil, err
	}
	if err := lockTrustFile(store.lock); err != nil {
		return nil, fmt.Errorf("Wi-Fi trust is in use by another host process: %w", err)
	}
	err = store.checkFile("trust.json")
	if errors.Is(err, os.ErrNotExist) {
		if !firstInitialization {
			return nil, errors.New("Wi-Fi identity record is missing from an existing store; restore it or explicitly reset trust locally")
		}
		if err := store.createIdentity(); err != nil {
			return nil, err
		}
		if err := store.save(store.record); err != nil {
			return nil, err
		}
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	file, err := root.Open("trust.json")
	if err != nil {
		return nil, err
	}
	body, readError := io.ReadAll(io.LimitReader(file, trustSizeLimit+1))
	if err := errors.Join(readError, file.Close()); err != nil {
		return nil, err
	}
	defer clear(body)
	if len(body) > trustSizeLimit {
		return nil, errors.New("Wi-Fi trust record exceeds 16 KiB; repair locally, identity was not replaced")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store.record); err != nil {
		return nil, fmt.Errorf("invalid Wi-Fi trust record; repair locally, identity was not replaced: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data in Wi-Fi trust record; identity was not replaced")
	}
	if store.record.Version != 1 || !validStoredPeer(store.record) {
		return nil, errors.New("unsupported or invalid Wi-Fi trust record; identity was not replaced")
	}
	secret, err := unprotectTrustKey(store.record.Key)
	if err != nil {
		return nil, fmt.Errorf("unlock Wi-Fi identity; repair locally, no plaintext fallback: %w", err)
	}
	defer clear(secret)
	key, err := x509.ParsePKCS8PrivateKey(secret)
	if err != nil {
		return nil, fmt.Errorf("invalid Wi-Fi private key; identity was not replaced: %w", err)
	}
	store.identity = tls.Certificate{Certificate: [][]byte{store.record.Cert}, PrivateKey: key}
	// Validate both the certificate policy and private/public key association.
	if _, err := PairedServerTLS(store.identity, [32]byte{1}); err != nil {
		return nil, fmt.Errorf("invalid or expired Wi-Fi host identity; re-pair locally: %w", err)
	}
	return store, nil
}

func (s *TrustStore) checkFile(name string) error {
	info, err := s.root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("Wi-Fi trust files must be regular files, not links or devices")
	}
	return secureStorePath(filepath.Join(s.root.Name(), name), false)
}

func (s *TrustStore) createIdentity() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "DrawSplay LAN host"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(5 * 365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	secret, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	defer clear(secret)
	protected, err := protectTrustKey(secret)
	if err != nil {
		return err
	}
	s.record = trustRecord{Version: 1, Cert: der, Key: protected}
	s.identity = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return nil
}

func validStoredPeer(record trustRecord) bool {
	if record.Peer == "" {
		return record.Label == ""
	}
	decoded, err := hex.DecodeString(record.Peer)
	return err == nil && len(decoded) == 32 && !bytes.Equal(decoded, make([]byte, 32)) && validTabletLabel(record.Label)
}

func validTabletLabel(label string) bool {
	return len(label) > 0 && len(label) <= 63 && utf8.ValidString(label) &&
		strings.IndexFunc(label, unicode.IsControl) < 0
}

func (s *TrustStore) Identity() tls.Certificate { return s.identity }

func (s *TrustStore) Peer() ([32]byte, bool) {
	if s.closed || s.record.Peer == "" {
		return [32]byte{}, false
	}
	decoded, err := hex.DecodeString(s.record.Peer)
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, false
	}
	return [32]byte(decoded), true
}

// Label is the paired tablet's saved name, or empty when unpaired.
func (s *TrustStore) Label() string {
	if _, paired := s.Peer(); !paired {
		return ""
	}
	return s.record.Label
}

func (s *TrustStore) Pair(pin [32]byte, label string) error {
	if pin == [32]byte{} || !validTabletLabel(label) {
		return errors.New("Wi-Fi pairing requires a nonzero identity and a printable tablet name of 1..63 bytes")
	}
	next := s.record
	next.Peer, next.Label = hex.EncodeToString(pin[:]), label
	return s.save(next)
}

func (s *TrustStore) Revoke() error {
	next := s.record
	next.Peer, next.Label = "", ""
	return s.save(next)
}

func (s *TrustStore) save(record trustRecord) (result error) {
	if s.closed {
		return os.ErrClosed
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	defer clear(data)
	if len(data) > trustSizeLimit {
		return errors.New("Wi-Fi trust record exceeds its fixed size limit")
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := "trust-" + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err := s.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	writeError := writeAll(file, data)
	syncError := file.Sync()
	if err := errors.Join(writeError, syncError, file.Close()); err != nil {
		return fmt.Errorf("persist Wi-Fi trust: %w", err)
	}
	if err := s.root.Rename(name, "trust.json"); err != nil {
		return fmt.Errorf("atomically replace Wi-Fi trust: %w", err)
	}
	s.record = record
	return syncTrustDirectory(s.root)
}

func (s *TrustStore) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var lockError, rootError error
	if s.lock != nil {
		lockError = s.lock.Close()
	}
	if s.root != nil {
		rootError = s.root.Close()
	}
	clear(s.record.Key)
	return errors.Join(lockError, rootError)
}

func IdentitySPKI(identity tls.Certificate) ([32]byte, error) {
	if len(identity.Certificate) == 0 {
		return [32]byte{}, ErrPairedIdentity
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(leaf.RawSubjectPublicKeyInfo), nil
}

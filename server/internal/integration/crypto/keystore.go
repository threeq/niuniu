package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// The integration master key is the decryption root for every integration
// credential (provider tokens, data sources, IM bots, git credentials). It
// lives in the OS secure store so it never touches disk in cleartext:
//
//	macOS   → Keychain
//	Windows → Credential Manager
//	Linux   → Secret Service (libsecret)
//
// Hosts without a Secret Service (headless Linux / minimal containers) fall
// back to a 0600 plaintext file — never silently: the fallback is reported in
// Outcome.Warnings and logged at startup.
const (
	// keychainService matches credstore so every niuniu secret lives under one
	// keychain application entry.
	keychainService = "niuniu-desktop"
	keychainAccount = "integration-master-key"

	// keyLen is the AES-256 master key size.
	keyLen = 32

	// EnvBackend forces the key storage backend: "keychain" (default) or
	// "file". Set it to "file" on hosts where the operator has decided the
	// keychain is unavailable or unwanted.
	EnvBackend = "NIUNIU_INTEGRATION_KEYRING_BACKEND"
	// EnvExportPath, when set, writes the resolved master key to that path
	// (mode 0600) once at startup — the one-time export / disaster-recovery
	// path for a keychain-stored key. Remove the file once backed up.
	EnvExportPath = "NIUNIU_INTEGRATION_KEYRING_EXPORT"
)

// ErrKeyNotFound reports that a KeyStore holds no key.
var ErrKeyNotFound = errors.New("crypto: master key not present")

// KeyStore is a persistent single-key store.
type KeyStore interface {
	// Get returns the stored key, or ErrKeyNotFound when absent. Any other
	// error means the backend is unusable.
	Get() ([]byte, error)
	Set(key []byte) error
	Delete() error
}

// Backend names where the master key ended up.
type Backend string

const (
	BackendKeychain Backend = "keychain"
	BackendFile     Backend = "file"
)

// Outcome describes how the master key was resolved.
type Outcome struct {
	Keyring  *Keyring
	Backend  Backend
	Migrated bool // the legacy plaintext file was consumed this run
	Degraded bool // keychain unusable → plaintext file fallback
	Warnings []string
}

// ExportToFile writes the master key to path with mode 0600. It exists so an
// operator can back up (or recover) a keychain-stored key once; the caller is
// responsible for deleting the file afterwards.
func (k *Keyring) ExportToFile(path string) error {
	if err := os.WriteFile(path, k.current, 0600); err != nil {
		return err
	}
	// WriteFile only applies the mode when it creates the file; chmod covers
	// the overwrite case.
	return os.Chmod(path, 0600)
}

// LoadIntegrationKeyring resolves the integration master key for a data
// directory, preferring the OS keychain and honouring the env overrides above.
func LoadIntegrationKeyring(dataDir string) (*Outcome, error) {
	legacyPath := filepath.Join(dataDir, "integration_secret")

	var kc KeyStore = NewKeychainStore()
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvBackend))) {
	case "", "keychain", "os":
		// default: OS secure store
	case "file":
		kc = nil
		slog.Warn("integration master key forced to the plaintext file backend; "+
			"local file-read protection is disabled on this host", "env", EnvBackend)
	default:
		return nil, fmt.Errorf("%s: unknown backend %q (want keychain|file)", EnvBackend, os.Getenv(EnvBackend))
	}

	out, err := LoadOrMigrate(kc, legacyPath)
	if err != nil {
		return nil, err
	}
	for _, w := range out.Warnings {
		slog.Warn("integration keyring", "detail", w)
	}
	if p := strings.TrimSpace(os.Getenv(EnvExportPath)); p != "" {
		if err := out.Keyring.ExportToFile(p); err != nil {
			return nil, fmt.Errorf("export integration key to %s: %w", p, err)
		}
		slog.Warn("integration master key exported to a plaintext file — "+
			"delete it once it is backed up", "path", p)
	}
	return out, nil
}

// LoadOrMigrate resolves the master key from kc, migrating the legacy plaintext
// file at legacyPath into the keychain on first run. Pass kc == nil to use the
// plaintext file explicitly (no keychain access at all).
//
// Resolution order:
//  1. keychain has a key      → use it; a matching leftover file is deleted
//  2. keychain is empty       → seed it from the file, else generate; delete file
//  3. keychain is unusable    → fall back to the file, explicitly degraded
func LoadOrMigrate(kc KeyStore, legacyPath string) (*Outcome, error) {
	file := FileStore{Path: legacyPath}

	if kc == nil {
		key, err := file.loadOrCreate()
		if err != nil {
			return nil, err
		}
		return &Outcome{Keyring: &Keyring{current: key}, Backend: BackendFile}, nil
	}

	key, err := kc.Get()
	switch {
	case err == nil:
		out := &Outcome{Keyring: &Keyring{current: key}, Backend: BackendKeychain}
		retireLegacyFile(file, key, out)
		return out, nil
	case errors.Is(err, ErrKeyNotFound):
		return seedKeychain(kc, file)
	default:
		return degradeToFile(file, err)
	}
}

// seedKeychain moves an existing file key (or a fresh one) into the keychain.
func seedKeychain(kc KeyStore, file FileStore) (*Outcome, error) {
	legacy, err := file.Get()
	fromFile := err == nil
	if err != nil && !errors.Is(err, ErrKeyNotFound) {
		return nil, err
	}

	key := legacy
	if !fromFile {
		if key, err = newKey(); err != nil {
			return nil, err
		}
	}

	if err := kc.Set(key); err != nil {
		return degradeToFile(file, fmt.Errorf("keychain write: %w", err))
	}
	// A keychain write can be silently dropped by some backends; verify before
	// we consider the plaintext file retired.
	got, err := kc.Get()
	if err != nil || !bytes.Equal(got, key) {
		return degradeToFile(file, fmt.Errorf("keychain write did not persist: %v", err))
	}

	out := &Outcome{Keyring: &Keyring{current: key}, Backend: BackendKeychain, Migrated: fromFile}
	if fromFile {
		if err := file.Delete(); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"integration master key moved into the OS keychain but the legacy plaintext file %s could not be removed: %v", file.Path, err))
		} else {
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"integration master key migrated from the plaintext file %s into the OS keychain; the plaintext file has been deleted", file.Path))
		}
	}
	return out, nil
}

// retireLegacyFile deletes a leftover plaintext file once the keychain is
// authoritative. A file holding a *different* key is kept for manual recovery.
func retireLegacyFile(file FileStore, keychainKey []byte, out *Outcome) {
	legacy, err := file.Get()
	if errors.Is(err, ErrKeyNotFound) {
		return
	}
	if err != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"could not inspect legacy plaintext file %s: %v", file.Path, err))
		return
	}
	if !bytes.Equal(legacy, keychainKey) {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"legacy plaintext file %s holds a DIFFERENT key than the OS keychain; "+
				"leaving it in place for manual recovery — the keychain key is authoritative", file.Path))
		return
	}
	if err := file.Delete(); err != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"could not remove leftover plaintext file %s: %v", file.Path, err))
		return
	}
	out.Migrated = true
	out.Warnings = append(out.Warnings, fmt.Sprintf(
		"removed leftover plaintext key file %s — the OS keychain is authoritative", file.Path))
}

// degradeToFile is the explicit, loud fallback for hosts without a usable OS
// keychain. It never runs silently: the reason is carried in Warnings.
func degradeToFile(file FileStore, cause error) (*Outcome, error) {
	key, err := file.loadOrCreate()
	if err != nil {
		return nil, err
	}
	return &Outcome{
		Keyring:  &Keyring{current: key},
		Backend:  BackendFile,
		Degraded: true,
		Warnings: []string{fmt.Sprintf(
			"OS keychain unavailable (%v): falling back to the plaintext key file %s. "+
				"Integration credentials are NOT protected against local file reads on this host", cause, file.Path)},
	}, nil
}

func newKey() ([]byte, error) {
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// KeychainStore persists the key in the OS secure store via go-keyring. The
// key is base64-encoded because keychain entries are strings.
type KeychainStore struct {
	Service string
	Account string
}

// NewKeychainStore returns a store bound to niuniu's keychain entry.
func NewKeychainStore() KeychainStore {
	return KeychainStore{Service: keychainService, Account: keychainAccount}
}

func (s KeychainStore) Get() ([]byte, error) {
	enc, err := keyring.Get(s.Service, s.Account)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("keyring get: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, fmt.Errorf("keyring entry is not base64: %w", err)
	}
	if len(key) != keyLen {
		return nil, fmt.Errorf("keyring key has wrong length: %d bytes", len(key))
	}
	return key, nil
}

func (s KeychainStore) Set(key []byte) error {
	if len(key) != keyLen {
		return fmt.Errorf("refusing to store a %d-byte key (want %d)", len(key), keyLen)
	}
	return keyring.Set(s.Service, s.Account, base64.StdEncoding.EncodeToString(key))
}

func (s KeychainStore) Delete() error {
	err := keyring.Delete(s.Service, s.Account)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// FileStore is the legacy plaintext backend: a 0600 file holding the raw key.
// It remains the explicit fallback for hosts without an OS keychain.
type FileStore struct {
	Path string
}

func (s FileStore) Get() ([]byte, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrKeyNotFound
		}
		return nil, fmt.Errorf("read key file %s: %w", s.Path, err)
	}
	if len(data) < keyLen {
		return nil, fmt.Errorf("key file %s too short: %d bytes", s.Path, len(data))
	}
	return data[:keyLen], nil
}

func (s FileStore) Set(key []byte) error {
	if err := os.WriteFile(s.Path, key, 0600); err != nil {
		return err
	}
	return os.Chmod(s.Path, 0600)
}

func (s FileStore) Delete() error {
	err := os.Remove(s.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// loadOrCreate returns the file's key, generating and persisting one when the
// file is absent.
func (s FileStore) loadOrCreate() ([]byte, error) {
	key, err := s.Get()
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, ErrKeyNotFound) {
		return nil, err
	}
	key, err = newKey()
	if err != nil {
		return nil, err
	}
	if err := s.Set(key); err != nil {
		return nil, fmt.Errorf("write key file %s: %w", s.Path, err)
	}
	return key, nil
}

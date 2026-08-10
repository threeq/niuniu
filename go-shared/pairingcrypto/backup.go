package pairingcrypto

import (
	"crypto/rand"
	"errors"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// BackupMeta captures the KDF parameters needed to re-derive the KEK during unwrap.
type BackupMeta struct {
	KDF       string `json:"kdf"`
	Salt      []byte `json:"salt"`
	Nonce     []byte `json:"nonce"`
	TimeCost  uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory"`
	Parallel  uint8  `json:"parallel"`
	Version   int    `json:"version"`
}

const (
	backupArgonTime    = 3
	backupArgonMemKiB  = 64 * 1024
	backupArgonThreads = 4
	backupKeyLen       = 32
	backupSaltLen      = 16
	backupNonceLen     = 24
)

// WrapIdentity encrypts id with a KEK derived from passphrase + accountID.
// The returned BackupMeta must be stored alongside the blob so unwrap can
// reproduce the KDF.
func WrapIdentity(passphrase, accountID string, id *Identity) ([]byte, *BackupMeta, error) {
	salt := make([]byte, backupSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, backupNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	kek := deriveKEK(passphrase, accountID, salt, backupArgonTime, backupArgonMemKiB, backupArgonThreads)
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, nil, err
	}
	aad := []byte("niuniu-backup-v1:" + accountID)
	ct := aead.Seal(nil, nonce, id.MarshalBinary(), aad)
	meta := &BackupMeta{
		KDF:       "argon2id",
		Salt:      salt,
		Nonce:     nonce,
		TimeCost:  backupArgonTime,
		MemoryKiB: backupArgonMemKiB,
		Parallel:  backupArgonThreads,
		Version:   1,
	}
	return ct, meta, nil
}

// UnwrapIdentity decrypts blob using passphrase + meta.
func UnwrapIdentity(passphrase, accountID string, blob []byte, meta *BackupMeta) (*Identity, error) {
	if meta.KDF != "argon2id" || meta.Version != 1 {
		return nil, errors.New("pairingcrypto: unsupported backup format")
	}
	kek := deriveKEK(passphrase, accountID, meta.Salt, meta.TimeCost, meta.MemoryKiB, meta.Parallel)
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, err
	}
	aad := []byte("niuniu-backup-v1:" + accountID)
	plain, err := aead.Open(nil, meta.Nonce, blob, aad)
	if err != nil {
		return nil, errors.New("pairingcrypto: backup open failed (wrong passphrase or tampered blob)")
	}
	return UnmarshalIdentity(plain)
}

func deriveKEK(passphrase, accountID string, salt []byte, t, m uint32, p uint8) []byte {
	input := []byte(passphrase + "\x00" + accountID)
	return argon2.IDKey(input, salt, t, m, p, backupKeyLen)
}

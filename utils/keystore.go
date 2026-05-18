package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/hkdf"
)

const (
	masterKeyEnv  = "TELECLOUD_MASTER_KEY"
	encPrefixV1   = "enc:v1:"
	keyByteLen    = 32
	gcmNonceBytes = 12
)

var (
	masterKey     []byte
	masterKeyOnce sync.Once
	masterKeyErr  error
)

// LoadMasterKey reads the TELECLOUD_MASTER_KEY env var and caches the decoded bytes.
// If the env var is not set, it attempts to load a cached key from a master.key file
// in the same directory as the database, or in the data/ directory, or in the current
// directory. If no key file exists, it generates a secure 32-byte key, saves it
// to the persistent file, and returns it.
func LoadMasterKey() ([]byte, error) {
	masterKeyOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv(masterKeyEnv))
		if raw == "" {
			// No env variable set. Determine the best location for a persistent key file.
			keyFile := "master.key"
			dbPath := strings.TrimSpace(os.Getenv("DATABASE_PATH"))
			if dbPath != "" {
				keyFile = filepath.Join(filepath.Dir(dbPath), "master.key")
			} else {
				// If a data directory exists, store it there
				if _, err := os.Stat("data"); err == nil {
					keyFile = filepath.Join("data", "master.key")
				}
			}

			// Try to read key from the file
			if data, err := os.ReadFile(keyFile); err == nil {
				keyStr := strings.TrimSpace(string(data))
				if key, err := decodeKey(keyStr); err == nil {
					masterKey = key
					return
				}
			}

			// File does not exist or is invalid. Generate a secure 32-byte key!
			newKeyBytes := make([]byte, keyByteLen)
			if _, err := io.ReadFull(rand.Reader, newKeyBytes); err != nil {
				masterKeyErr = fmt.Errorf("failed to generate secure master key: %w", err)
				return
			}

			// Encode as 64-char hex
			newKeyHex := hex.EncodeToString(newKeyBytes)

			// Ensure target directory exists before writing
			if err := os.MkdirAll(filepath.Dir(keyFile), 0755); err != nil {
				masterKeyErr = fmt.Errorf("failed to create directory for master key file: %w", err)
				return
			}

			// Save to file with restrictive permissions (0600)
			if err := os.WriteFile(keyFile, []byte(newKeyHex), 0600); err != nil {
				masterKeyErr = fmt.Errorf("failed to save generated master key to %s: %w", keyFile, err)
				return
			}

			log.Printf("[MasterKey] Auto-generated secure master key and saved to %s", keyFile)
			masterKey = newKeyBytes
			return
		}

		key, err := decodeKey(raw)
		if err != nil {
			masterKeyErr = fmt.Errorf("%s is invalid: %w", masterKeyEnv, err)
			return
		}
		masterKey = key
	})
	if masterKeyErr != nil {
		return nil, masterKeyErr
	}
	return masterKey, nil
}

// MasterKeyLoaded reports whether a master key has been successfully loaded.
func MasterKeyLoaded() bool {
	return masterKey != nil
}

func decodeKey(raw string) ([]byte, error) {
	if b, err := hex.DecodeString(raw); err == nil && len(b) == keyByteLen {
		return b, nil
	}
	for _, dec := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := dec.DecodeString(raw); err == nil && len(b) == keyByteLen {
			return b, nil
		}
	}
	return nil, fmt.Errorf("expected 32 bytes encoded as hex(64 chars) or base64")
}

// DeriveSubKey returns a 32-byte sub-key derived from the master key using HKDF-SHA256
// with the given label as the info parameter. Useful for separating concerns (HMAC vs AEAD vs ...).
func DeriveSubKey(label string) ([]byte, error) {
	mk, err := LoadMasterKey()
	if err != nil {
		return nil, err
	}
	r := hkdf.New(sha256.New, mk, nil, []byte(label))
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// EncryptAEAD encrypts plaintext with AES-256-GCM using the master key.
// Output layout: nonce(12) || ciphertext || tag(16).
func EncryptAEAD(plaintext []byte) ([]byte, error) {
	mk, err := LoadMasterKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(mk)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// DecryptAEAD reverses EncryptAEAD.
func DecryptAEAD(blob []byte) ([]byte, error) {
	if len(blob) < gcmNonceBytes+16 {
		return nil, errors.New("ciphertext too short")
	}
	mk, err := LoadMasterKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(mk)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := blob[:gcm.NonceSize()]
	ct := blob[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

// EncryptString returns "enc:v1:<base64(EncryptAEAD(s))>".
func EncryptString(s string) (string, error) {
	enc, err := EncryptAEAD([]byte(s))
	if err != nil {
		return "", err
	}
	return encPrefixV1 + base64.RawStdEncoding.EncodeToString(enc), nil
}

// DecryptString reverses EncryptString. Values without the "enc:v1:" prefix are
// returned as-is (so callers can read legacy plaintext rows; auto-migration is
// responsible for re-encrypting them).
func DecryptString(s string) (string, error) {
	if !strings.HasPrefix(s, encPrefixV1) {
		return s, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(s, encPrefixV1))
	if err != nil {
		return "", err
	}
	plain, err := DecryptAEAD(raw)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// IsEncryptedString reports whether s carries the encryption prefix.
func IsEncryptedString(s string) bool {
	return strings.HasPrefix(s, encPrefixV1)
}

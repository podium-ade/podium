// Package secrets is the control plane's encrypted secret store: the master key, the
// AES-256-GCM envelope around every stored value, and the resolution step that turns a
// task's SecretRefs into the plaintext an Assign carries to a node.
//
// Nothing in this package logs a value, a key or a decrypted blob. The only thing that
// ever comes back out of it is a [Resolved], and that goes straight into an Assign.
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// KeySize is the master key length. AES-256 takes nothing else.
const KeySize = 32

// KeyFileMode is the most permissive mode a master key file may have. The check is on the
// group and other bits, so 0400 is accepted too; anything a second account on the machine
// can read is refused.
const KeyFileMode fs.FileMode = 0o600

// ErrNoKey is returned when a secrets operation is attempted on a server that was started
// without a master key.
var ErrNoKey = errors.New("secrets: no master key configured (set PODIUM_MASTER_KEY_FILE)")

// ErrDecrypt is what a wrong master key, a corrupted ciphertext or a re-labelled row all
// come back as. It is deliberately one error: distinguishing them for a caller would tell
// an attacker which of the three they achieved.
var ErrDecrypt = errors.New("secrets: decrypt failed (wrong master key, or the stored value was tampered with)")

// Key is a loaded master key. It is immutable and safe for concurrent use.
type Key struct {
	raw  []byte
	id   string
	aead cipher.AEAD
}

// NewKey wraps 32 raw bytes. The caller must not reuse the slice afterwards.
func NewKey(raw []byte) (*Key, error) {
	if len(raw) != KeySize {
		return nil, fmt.Errorf("secrets: master key must be %d bytes, got %d", KeySize, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("secrets: build cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: build gcm: %w", err)
	}
	sum := sha256.Sum256(raw)
	return &Key{raw: bytes.Clone(raw), id: hex.EncodeToString(sum[:8]), aead: aead}, nil
}

// GenerateKey mints a fresh master key from crypto/rand.
func GenerateKey() (*Key, error) {
	raw := make([]byte, KeySize)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("secrets: generate master key: %w", err)
	}
	return NewKey(raw)
}

// ParseKey accepts either 64 hex characters, with surrounding whitespace ignored, or 32
// raw bytes. Both are what a `podium-server gen-master-key` file could plausibly contain
// after passing through an editor or a secret manager; hex is what gen-master-key writes.
func ParseKey(b []byte) (*Key, error) {
	if text := strings.TrimSpace(string(b)); len(text) == KeySize*2 {
		raw, err := hex.DecodeString(text)
		if err != nil {
			return nil, fmt.Errorf("secrets: master key is %d characters but is not hex: %w", KeySize*2, err)
		}
		return NewKey(raw)
	}
	if len(b) == KeySize {
		return NewKey(b)
	}
	return nil, fmt.Errorf("secrets: master key must be %d hex characters or %d raw bytes, got %d bytes",
		KeySize*2, KeySize, len(b))
}

// LoadKeyFile reads a master key from disk. It refuses a file any other account on the
// machine can read: a master key that leaks decrypts every secret Podium holds, and a
// stray `chmod 644` is the likeliest way for that to happen.
func LoadKeyFile(path string) (*Key, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: master key file %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("secrets: master key file %s is a directory", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf(
			"secrets: master key file %s is mode %04o and is readable by other accounts on this machine; run `chmod %04o %s`",
			path, perm, KeyFileMode, path)
	}
	b, err := os.ReadFile(path) //nolint:gosec // the operator names their own key file
	if err != nil {
		return nil, fmt.Errorf("secrets: read master key file %s: %w", path, err)
	}
	key, err := ParseKey(b)
	if err != nil {
		return nil, fmt.Errorf("%w (file %s)", err, path)
	}
	return key, nil
}

// WriteKeyFile writes a key to a new file with mode 0600. It refuses to overwrite: losing
// a master key loses every secret encrypted under it, so replacing one is a deliberate
// two-step (rotate, then remove).
func WriteKeyFile(path string, key *Key) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, KeyFileMode)
	if err != nil {
		return fmt.Errorf("secrets: write master key file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(key.Hex() + "\n"); err != nil {
		return fmt.Errorf("secrets: write master key file %s: %w", path, err)
	}
	return nil
}

// ID identifies the key without revealing it: the first 8 bytes of its SHA-256, hex
// encoded. It is stored on every row so a half-finished rotation is visible.
func (k *Key) ID() string { return k.id }

// Hex is the key's file form. It is the only method that reveals key material and exists
// solely for gen-master-key; never log its result.
func (k *Key) Hex() string { return hex.EncodeToString(k.raw) }

// Equal reports whether two keys are the same key, in constant time.
func (k *Key) Equal(other *Key) bool {
	if k == nil || other == nil {
		return k == other
	}
	return subtle.ConstantTimeCompare(k.raw, other.raw) == 1
}

// Encrypt seals value under the master key with a fresh random nonce, binding it to name
// as additional authenticated data: a ciphertext moved to another row fails to decrypt
// rather than silently becoming a different secret.
func (k *Key) Encrypt(name string, value []byte) (ciphertext, nonce []byte, err error) {
	if k == nil {
		return nil, nil, ErrNoKey
	}
	if name == "" {
		return nil, nil, errors.New("secrets: encrypt: name is required")
	}
	nonce = make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("secrets: generate nonce: %w", err)
	}
	return k.aead.Seal(nil, nonce, value, []byte(name)), nonce, nil
}

// Decrypt opens a stored value. Every failure — a wrong key, a truncated ciphertext, a row
// whose name does not match the one it was sealed under — is ErrDecrypt.
func (k *Key) Decrypt(name string, ciphertext, nonce []byte) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKey
	}
	if len(nonce) != k.aead.NonceSize() {
		return nil, fmt.Errorf("%w: nonce is %d bytes, want %d", ErrDecrypt, len(nonce), k.aead.NonceSize())
	}
	value, err := k.aead.Open(nil, nonce, ciphertext, []byte(name))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDecrypt, name)
	}
	return value, nil
}

// Zero overwrites b. It is what the node and the API use on a plaintext they are finished
// with; it is not a guarantee (Go may have copied the bytes already) but it shortens the
// window in which a core dump contains a live credential.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

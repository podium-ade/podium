package secrets

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKeyHex is an obvious fake: 32 bytes of 0xAB. Never use it for anything real.
const testKeyHex = "abababababababababababababababababababababababababababababababab"

func writeKey(t *testing.T, name, contents string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(contents), mode))
	// WriteFile is subject to the process umask; force the mode we are testing.
	require.NoError(t, os.Chmod(path, mode))
	return path
}

func TestGenerateKeyIsDistinctAndRightSized(t *testing.T) {
	a, err := GenerateKey()
	require.NoError(t, err)
	b, err := GenerateKey()
	require.NoError(t, err)

	assert.Len(t, a.Hex(), KeySize*2)
	assert.False(t, a.Equal(b), "two generated keys must not collide")
	assert.NotEqual(t, a.ID(), b.ID())
	assert.True(t, a.Equal(a))
}

func TestParseKeyAcceptsHexAndRawBytes(t *testing.T) {
	raw, err := hex.DecodeString(testKeyHex)
	require.NoError(t, err)

	fromHex, err := ParseKey([]byte(testKeyHex + "\n"))
	require.NoError(t, err, "a trailing newline is what an editor leaves behind")
	fromRaw, err := ParseKey(raw)
	require.NoError(t, err)
	assert.True(t, fromHex.Equal(fromRaw), "both spellings are the same key")

	_, err = ParseKey([]byte("too short"))
	require.ErrorContains(t, err, "must be 64 hex characters or 32 raw bytes")

	_, err = ParseKey([]byte(strings.Repeat("z", 64)))
	require.ErrorContains(t, err, "is not hex")
}

// A key file another account can read is a key file that will eventually be read. The
// server refuses to start rather than encrypt under it.
func TestLoadKeyFileRefusesAWorldReadableFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o666, 0o444} {
		path := writeKey(t, "master.key", testKeyHex, mode)
		_, err := LoadKeyFile(path)
		require.Error(t, err, "mode %04o must be refused", mode)
		assert.Contains(t, err.Error(), "readable by other accounts")
		assert.Contains(t, err.Error(), "chmod 0600")
	}
}

func TestLoadKeyFileAcceptsOwnerOnlyModes(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		path := writeKey(t, "master.key", testKeyHex, mode)
		key, err := LoadKeyFile(path)
		require.NoError(t, err, "mode %04o must be accepted", mode)
		assert.Equal(t, testKeyHex, key.Hex())
	}
}

func TestLoadKeyFileReportsAMissingFile(t *testing.T) {
	_, err := LoadKeyFile(filepath.Join(t.TempDir(), "absent"))
	require.ErrorContains(t, err, "master key file")
}

func TestWriteKeyFileIsOwnerOnlyAndRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	key, err := GenerateKey()
	require.NoError(t, err)
	require.NoError(t, WriteKeyFile(path, key))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	loaded, err := LoadKeyFile(path)
	require.NoError(t, err)
	assert.True(t, key.Equal(loaded))

	require.Error(t, WriteKeyFile(path, key), "overwriting a master key loses every secret under it")
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key, err := ParseKey([]byte(testKeyHex))
	require.NoError(t, err)

	value := []byte("hunter2-hunter2-hunter2")
	ciphertext, nonce, err := key.Encrypt("GREETING", value)
	require.NoError(t, err)
	assert.Len(t, nonce, 12)
	assert.NotContains(t, string(ciphertext), string(value), "the plaintext must not be in the ciphertext")

	got, err := key.Decrypt("GREETING", ciphertext, nonce)
	require.NoError(t, err)
	assert.Equal(t, value, got)
}

func TestEncryptUsesAFreshNoncePerCall(t *testing.T) {
	key, err := GenerateKey()
	require.NoError(t, err)

	c1, n1, err := key.Encrypt("A", []byte("same value"))
	require.NoError(t, err)
	c2, n2, err := key.Encrypt("A", []byte("same value"))
	require.NoError(t, err)

	assert.NotEqual(t, n1, n2, "GCM nonce reuse under one key is catastrophic")
	assert.NotEqual(t, c1, c2)
}

// The name is additional authenticated data, so a ciphertext cannot be re-labelled: moving
// a row from DB_PASSWORD to GREETING makes it undecryptable rather than making the
// attacker's chosen value answer to a name they picked.
func TestDecryptRejectsARelabelledCiphertext(t *testing.T) {
	key, err := GenerateKey()
	require.NoError(t, err)
	ciphertext, nonce, err := key.Encrypt("DB_PASSWORD", []byte("hunter2-hunter2"))
	require.NoError(t, err)

	_, err = key.Decrypt("GREETING", ciphertext, nonce)
	require.ErrorIs(t, err, ErrDecrypt)
}

// A wrong master key is a clean error, never garbage: GCM authenticates before it returns
// anything, so there is no way to get plausible-looking wrong plaintext out of it.
func TestDecryptWithTheWrongKeyIsAnErrorNotGarbage(t *testing.T) {
	right, err := GenerateKey()
	require.NoError(t, err)
	wrong, err := GenerateKey()
	require.NoError(t, err)

	ciphertext, nonce, err := right.Encrypt("GREETING", []byte("hunter2-hunter2"))
	require.NoError(t, err)

	got, err := wrong.Decrypt("GREETING", ciphertext, nonce)
	require.ErrorIs(t, err, ErrDecrypt)
	assert.Nil(t, got, "a failed decrypt returns nothing at all")
}

func TestDecryptRejectsATamperedCiphertextAndABadNonce(t *testing.T) {
	key, err := GenerateKey()
	require.NoError(t, err)
	ciphertext, nonce, err := key.Encrypt("GREETING", []byte("hunter2-hunter2"))
	require.NoError(t, err)

	tampered := bytes.Clone(ciphertext)
	tampered[0] ^= 0xff
	_, err = key.Decrypt("GREETING", tampered, nonce)
	require.ErrorIs(t, err, ErrDecrypt)

	_, err = key.Decrypt("GREETING", ciphertext, nonce[:6])
	require.ErrorIs(t, err, ErrDecrypt)
}

func TestNilKeyIsDisabledNotPanicking(t *testing.T) {
	var key *Key
	_, _, err := key.Encrypt("A", []byte("x"))
	require.ErrorIs(t, err, ErrNoKey)
	_, err = key.Decrypt("A", []byte("x"), make([]byte, 12))
	require.ErrorIs(t, err, ErrNoKey)
	assert.True(t, key.Equal(nil), "two nil keys are the same absence of a key")
}

func TestZeroScrubs(t *testing.T) {
	b := []byte("hunter2")
	Zero(b)
	assert.Equal(t, make([]byte, 7), b)
}

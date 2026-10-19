// Package credentialcrypto encrypts provider credentials with AES-256-GCM
// using keys obtained from a KeyRing. Ciphertexts are bound to their
// workspace, provider and key version through additional authenticated data.
package credentialcrypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/OcheOps/voxa/server/internal/domain"
)

const (
	// KeySize is the required AES-256 key length in bytes.
	KeySize = 32
	// NonceSize is the standard GCM nonce length in bytes.
	NonceSize = 12
	// aadLabel identifies the record format inside the AAD. Changing the
	// format requires a new label, never a reinterpretation of this one.
	aadLabel = "voxa.credential.v1"
)

var (
	ErrInvalidKey        = errors.New("credential encryption key is invalid: must be base64 (standard, padded) encoding of exactly 32 bytes")
	ErrInvalidKeyVersion = errors.New("credential key version is invalid: must match [A-Za-z0-9][A-Za-z0-9._-]{0,31}")
	ErrUnknownKeyVersion = errors.New("credential key version is not available in the key ring")
	ErrDecrypt           = errors.New("credential decryption failed: authentication check did not pass")
	ErrMalformed         = errors.New("credential record is malformed")
)

var keyVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// ValidateKeyVersion checks the textual key-version identifier. Versions are
// non-secret metadata: they are stored in plaintext beside each ciphertext.
func ValidateKeyVersion(version string) error {
	if !keyVersionPattern.MatchString(version) {
		return ErrInvalidKeyVersion
	}
	return nil
}

// ParseKey decodes base64 key material and requires exactly 32 bytes. Errors
// never echo the input.
func ParseKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(key) != KeySize {
		return nil, ErrInvalidKey
	}
	return key, nil
}

// KeyRing supplies the active encryption key version and AEADs by version.
// The static implementation holds keys in process memory. A KMS or Vault
// implementation could unwrap per-version data keys at startup, or call a
// remote service, behind the same interface.
type KeyRing interface {
	ActiveVersion() string
	AEAD(ctx context.Context, version string) (cipher.AEAD, error)
}

// StaticKeyRing is an in-memory KeyRing built from explicit key material.
type StaticKeyRing struct {
	active string
	aeads  map[string]cipher.AEAD
}

// NewStaticKeyRing builds a ring with an active version and any number of
// additional decrypt-only versions. The active version must be present in
// keys. Key bytes are not retained beyond the AES key schedule.
func NewStaticKeyRing(active string, keys map[string][]byte) (*StaticKeyRing, error) {
	if err := ValidateKeyVersion(active); err != nil {
		return nil, err
	}
	if _, ok := keys[active]; !ok {
		return nil, fmt.Errorf("active credential key version %q has no key", active)
	}
	ring := &StaticKeyRing{active: active, aeads: make(map[string]cipher.AEAD, len(keys))}
	for version, key := range keys {
		if err := ValidateKeyVersion(version); err != nil {
			return nil, err
		}
		if len(key) != KeySize {
			return nil, ErrInvalidKey
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, ErrInvalidKey
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, ErrInvalidKey
		}
		ring.aeads[version] = aead
	}
	return ring, nil
}

func (r *StaticKeyRing) ActiveVersion() string { return r.active }

func (r *StaticKeyRing) AEAD(_ context.Context, version string) (cipher.AEAD, error) {
	aead, ok := r.aeads[version]
	if !ok {
		return nil, ErrUnknownKeyVersion
	}
	return aead, nil
}

// Versions lists every loaded key version, sorted.
func (r *StaticKeyRing) Versions() []string {
	versions := make([]string, 0, len(r.aeads))
	for version := range r.aeads {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions
}

// Sealed is a self-describing encrypted credential: with the right key
// version available it can be decrypted on its own.
type Sealed struct {
	KeyVersion string
	Nonce      []byte
	Ciphertext []byte // includes the 16-byte GCM tag
}

// Cipher seals and opens credential secrets.
type Cipher struct {
	keys KeyRing
}

func NewCipher(keys KeyRing) *Cipher { return &Cipher{keys: keys} }

func (c *Cipher) ActiveVersion() string { return c.keys.ActiveVersion() }

// Seal encrypts secret under the active key with a fresh random nonce.
func (c *Cipher) Seal(ctx context.Context, workspaceID domain.WorkspaceID, provider string, secret domain.CredentialSecret) (Sealed, error) {
	version := c.keys.ActiveVersion()
	aead, err := c.keys.AEAD(ctx, version)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, secret.Expose(), AdditionalData(workspaceID, provider, version))
	return Sealed{KeyVersion: version, Nonce: nonce, Ciphertext: ciphertext}, nil
}

// Open decrypts a sealed credential using the key named by its stored
// version. Errors are ErrUnknownKeyVersion, ErrMalformed or ErrDecrypt and
// never contain key or plaintext material.
func (c *Cipher) Open(ctx context.Context, workspaceID domain.WorkspaceID, provider string, sealed Sealed) (domain.CredentialSecret, error) {
	if ValidateKeyVersion(sealed.KeyVersion) != nil || len(sealed.Nonce) != NonceSize {
		return domain.CredentialSecret{}, ErrMalformed
	}
	aead, err := c.keys.AEAD(ctx, sealed.KeyVersion)
	if err != nil {
		return domain.CredentialSecret{}, err
	}
	if len(sealed.Ciphertext) < aead.Overhead() {
		return domain.CredentialSecret{}, ErrMalformed
	}
	plaintext, err := aead.Open(nil, sealed.Nonce, sealed.Ciphertext, AdditionalData(workspaceID, provider, sealed.KeyVersion))
	if err != nil {
		return domain.CredentialSecret{}, ErrDecrypt
	}
	return domain.NewCredentialSecret(plaintext), nil
}

// AdditionalData builds the GCM AAD that binds a ciphertext to its logical
// identity. Each field is written as a 4-byte big-endian length followed by
// its bytes, in this order:
//
//	"voxa.credential.v1" | workspace ID | provider | key version
//
// The length prefixes make the encoding unambiguous: no two field tuples
// produce the same bytes.
func AdditionalData(workspaceID domain.WorkspaceID, provider, keyVersion string) []byte {
	fields := []string{aadLabel, string(workspaceID), provider, keyVersion}
	size := 0
	for _, field := range fields {
		size += 4 + len(field)
	}
	aad := make([]byte, 0, size)
	for _, field := range fields {
		aad = binary.BigEndian.AppendUint32(aad, uint32(len(field)))
		aad = append(aad, field...)
	}
	return aad
}

package credentialcrypto_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/OcheOps/voxa/server/internal/credentialcrypto"
	"github.com/OcheOps/voxa/server/internal/domain"
)

const fakeSecret = "sk-VOXA-TEST-SECRET-must-never-leak"

func randomKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, credentialcrypto.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func newCipher(t *testing.T, active string, keys map[string][]byte) *credentialcrypto.Cipher {
	t.Helper()
	ring, err := credentialcrypto.NewStaticKeyRing(active, keys)
	if err != nil {
		t.Fatal(err)
	}
	return credentialcrypto.NewCipher(ring)
}

func secret() domain.CredentialSecret { return domain.NewCredentialSecret([]byte(fakeSecret)) }

func TestSealOpenRoundTrip(t *testing.T) {
	c := newCipher(t, "v1", map[string][]byte{"v1": randomKey(t)})
	sealed, err := c.Seal(context.Background(), "ws-a", "openai", secret())
	if err != nil {
		t.Fatal(err)
	}
	if sealed.KeyVersion != "v1" || len(sealed.Nonce) != credentialcrypto.NonceSize {
		t.Fatalf("sealed metadata = %q nonce=%d", sealed.KeyVersion, len(sealed.Nonce))
	}
	if bytes.Contains(sealed.Ciphertext, []byte(fakeSecret)) {
		t.Fatal("ciphertext contains plaintext")
	}
	opened, err := c.Open(context.Background(), "ws-a", "openai", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened.Expose()) != fakeSecret {
		t.Fatal("round trip mismatch")
	}
}

func TestSealIsNonDeterministic(t *testing.T) {
	c := newCipher(t, "v1", map[string][]byte{"v1": randomKey(t)})
	first, err := c.Seal(context.Background(), "ws-a", "openai", secret())
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Seal(context.Background(), "ws-a", "openai", secret())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Nonce, second.Nonce) || bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Fatal("same plaintext produced identical nonce or ciphertext")
	}
}

func TestOpenRejectsWrongContextKeyAndTampering(t *testing.T) {
	ctx := context.Background()
	key := randomKey(t)
	c := newCipher(t, "v1", map[string][]byte{"v1": key})
	sealed, err := c.Seal(ctx, "ws-a", "openai", secret())
	if err != nil {
		t.Fatal(err)
	}
	flip := func(b []byte, i int) []byte {
		out := bytes.Clone(b)
		out[i] ^= 0x01
		return out
	}
	otherKey := newCipher(t, "v1", map[string][]byte{"v1": randomKey(t)})
	otherVersionOnly := newCipher(t, "v2", map[string][]byte{"v2": key})

	cases := []struct {
		name      string
		cipher    *credentialcrypto.Cipher
		workspace domain.WorkspaceID
		provider  string
		sealed    credentialcrypto.Sealed
		want      error
	}{
		{"wrong workspace", c, "ws-b", "openai", sealed, credentialcrypto.ErrDecrypt},
		{"wrong provider", c, "ws-a", "anthropic", sealed, credentialcrypto.ErrDecrypt},
		{"wrong key", otherKey, "ws-a", "openai", sealed, credentialcrypto.ErrDecrypt},
		{"unknown key version", otherVersionOnly, "ws-a", "openai", sealed, credentialcrypto.ErrUnknownKeyVersion},
		{"relabelled key version", c, "ws-a", "openai", credentialcrypto.Sealed{KeyVersion: "v2", Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext}, credentialcrypto.ErrUnknownKeyVersion},
		{"modified ciphertext", c, "ws-a", "openai", credentialcrypto.Sealed{KeyVersion: "v1", Nonce: sealed.Nonce, Ciphertext: flip(sealed.Ciphertext, 0)}, credentialcrypto.ErrDecrypt},
		{"modified tag", c, "ws-a", "openai", credentialcrypto.Sealed{KeyVersion: "v1", Nonce: sealed.Nonce, Ciphertext: flip(sealed.Ciphertext, len(sealed.Ciphertext)-1)}, credentialcrypto.ErrDecrypt},
		{"modified nonce", c, "ws-a", "openai", credentialcrypto.Sealed{KeyVersion: "v1", Nonce: flip(sealed.Nonce, 0), Ciphertext: sealed.Ciphertext}, credentialcrypto.ErrDecrypt},
		{"short nonce", c, "ws-a", "openai", credentialcrypto.Sealed{KeyVersion: "v1", Nonce: sealed.Nonce[:8], Ciphertext: sealed.Ciphertext}, credentialcrypto.ErrMalformed},
		{"truncated ciphertext", c, "ws-a", "openai", credentialcrypto.Sealed{KeyVersion: "v1", Nonce: sealed.Nonce, Ciphertext: sealed.Ciphertext[:4]}, credentialcrypto.ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opened, err := tc.cipher.Open(ctx, tc.workspace, tc.provider, tc.sealed)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(opened.Expose()) != 0 {
				t.Fatal("plaintext returned on failure")
			}
			if strings.Contains(err.Error(), fakeSecret) {
				t.Fatal("error leaked plaintext")
			}
		})
	}
}

func TestAdditionalDataIsUnambiguous(t *testing.T) {
	// Without length prefixes these two would concatenate identically.
	a := credentialcrypto.AdditionalData("ab", "c", "v1")
	b := credentialcrypto.AdditionalData("a", "bc", "v1")
	if bytes.Equal(a, b) {
		t.Fatal("AAD encoding is ambiguous")
	}
	if !bytes.Equal(a, credentialcrypto.AdditionalData("ab", "c", "v1")) {
		t.Fatal("AAD encoding is not deterministic")
	}
}

func TestParseKeyValidation(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(randomKey(t))
	if key, err := credentialcrypto.ParseKey(valid); err != nil || len(key) != 32 {
		t.Fatalf("valid key rejected: %v", err)
	}
	invalid := map[string]string{
		"empty":       "",
		"passphrase":  "correct horse battery staple TOPSECRETINPUT",
		"short":       base64.StdEncoding.EncodeToString(make([]byte, 16)),
		"long":        base64.StdEncoding.EncodeToString(make([]byte, 33)),
		"hex":         strings.Repeat("ab", 32),
		"unpadded":    base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
		"url-encoded": strings.ReplaceAll(strings.ReplaceAll(valid, "+", "-"), "/", "_") + "TOPSECRETINPUT",
	}
	for name, input := range invalid {
		_, err := credentialcrypto.ParseKey(input)
		if !errors.Is(err, credentialcrypto.ErrInvalidKey) {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if input != "" && strings.Contains(err.Error(), input) || strings.Contains(err.Error(), "TOPSECRETINPUT") {
			t.Errorf("%s: error echoed key input", name)
		}
	}
}

func TestKeyRingValidation(t *testing.T) {
	key := randomKey(t)
	encoded := base64.StdEncoding.EncodeToString(key)
	cases := map[string]struct {
		active string
		keys   map[string][]byte
	}{
		"blank version":      {"", map[string][]byte{"": key}},
		"bad version":        {"v 1", map[string][]byte{"v 1": key}},
		"missing active key": {"v2", map[string][]byte{"v1": key}},
		"short key":          {"v1", map[string][]byte{"v1": key[:16]}},
		"bad extra version":  {"v1", map[string][]byte{"v1": key, "bad/version": key}},
	}
	for name, tc := range cases {
		_, err := credentialcrypto.NewStaticKeyRing(tc.active, tc.keys)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), encoded) || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), string(key)) {
			t.Errorf("%s: error leaked key material", name)
		}
	}
}

func TestDecryptWithPreviousKeyAfterRotation(t *testing.T) {
	ctx := context.Background()
	oldKey, newKey := randomKey(t), randomKey(t)
	before := newCipher(t, "v1", map[string][]byte{"v1": oldKey})
	sealed, err := before.Seal(ctx, "ws-a", "openai", secret())
	if err != nil {
		t.Fatal(err)
	}
	after := newCipher(t, "v2", map[string][]byte{"v1": oldKey, "v2": newKey})
	opened, err := after.Open(ctx, "ws-a", "openai", sealed)
	if err != nil || string(opened.Expose()) != fakeSecret {
		t.Fatalf("old row unreadable after rotation: %v", err)
	}
	resealed, err := after.Seal(ctx, "ws-a", "openai", opened)
	if err != nil || resealed.KeyVersion != "v2" {
		t.Fatalf("new writes not using active key: %q %v", resealed.KeyVersion, err)
	}
}

func TestCredentialSecretNeverFormats(t *testing.T) {
	s := secret()
	outputs := []string{
		fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q %x %X", s, s, s, s, s, s, s),
		fmt.Sprintf("%v", struct{ S domain.CredentialSecret }{s}), s.String(), s.GoString(), s.LogValue().String(),
	}
	encoded, err := s.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(encoded))
	for _, output := range outputs {
		if strings.Contains(output, fakeSecret) || strings.Contains(output, fmt.Sprintf("%x", fakeSecret)) {
			t.Fatalf("secret leaked via formatting: %s", output)
		}
	}
	s.Wipe()
	if bytes.Contains(s.Expose(), []byte(fakeSecret)) {
		t.Fatal("wipe did not clear bytes")
	}
}

package domain

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

const redacted = "[REDACTED]"

// CredentialSecret holds raw provider credential material. Every formatting,
// logging and JSON-encoding path prints a redaction marker instead of the
// value, so the secret is not exposed by accident. Use Expose only at the
// exact point where the bytes are needed (encryption, a provider call).
//
// This reduces accidental exposure. It is not secure memory: Go may copy
// the bytes, and Wipe is best-effort only.
type CredentialSecret struct {
	value []byte
}

func NewCredentialSecret(value []byte) CredentialSecret {
	return CredentialSecret{value: value}
}

// Expose returns the underlying bytes. Callers must not log, format, or
// retain them.
func (s CredentialSecret) Expose() []byte { return s.value }

// IsBlank reports whether the secret is empty or whitespace only.
func (s CredentialSecret) IsBlank() bool { return strings.TrimSpace(string(s.value)) == "" }

// Wipe zeroes the underlying bytes (best effort).
func (s CredentialSecret) Wipe() {
	for i := range s.value {
		s.value[i] = 0
	}
}

func (CredentialSecret) String() string               { return redacted }
func (CredentialSecret) GoString() string             { return redacted }
func (CredentialSecret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (CredentialSecret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// Format redacts every fmt verb, including %x, %q and %#v.
func (CredentialSecret) Format(state fmt.State, _ rune) { _, _ = state.Write([]byte(redacted)) }

// UnmarshalJSON accepts a JSON string, so request DTOs can decode directly
// into a secret without holding a plain string field.
func (s *CredentialSecret) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("credential value must be a string")
	}
	s.value = []byte(value)
	return nil
}

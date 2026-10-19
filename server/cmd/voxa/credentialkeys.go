package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/OcheOps/voxa/server/internal/credentialcrypto"
)

const (
	credentialKeyEnv          = "VOXA_CREDENTIAL_KEY"
	credentialKeyVersionEnv   = "VOXA_CREDENTIAL_KEY_VERSION"
	credentialPreviousKeysEnv = "VOXA_CREDENTIAL_PREVIOUS_KEYS"
)

// loadCredentialKeyRing builds the credential key ring from the environment:
//
//	VOXA_CREDENTIAL_KEY            base64 of exactly 32 random bytes (active key)
//	VOXA_CREDENTIAL_KEY_VERSION    identifier stored with every row it seals
//	VOXA_CREDENTIAL_PREVIOUS_KEYS  optional decrypt-only keys, "version:base64,..."
//
// Errors name the variable (and entry index) but never echo values.
func loadCredentialKeyRing(getenv func(string) string) (*credentialcrypto.StaticKeyRing, error) {
	encoded := getenv(credentialKeyEnv)
	version := strings.TrimSpace(getenv(credentialKeyVersionEnv))
	if strings.TrimSpace(encoded) == "" {
		return nil, fmt.Errorf("%s is required when %s is set", credentialKeyEnv, "VOXA_DATABASE_URL")
	}
	if version == "" {
		return nil, fmt.Errorf("%s is required when %s is set", credentialKeyVersionEnv, credentialKeyEnv)
	}
	if err := credentialcrypto.ValidateKeyVersion(version); err != nil {
		return nil, fmt.Errorf("%s: %w", credentialKeyVersionEnv, err)
	}
	active, err := credentialcrypto.ParseKey(encoded)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", credentialKeyEnv, err)
	}
	keys := map[string][]byte{version: active}

	if previous := strings.TrimSpace(getenv(credentialPreviousKeysEnv)); previous != "" {
		for index, entry := range strings.Split(previous, ",") {
			entryVersion, entryKey, found := strings.Cut(strings.TrimSpace(entry), ":")
			if !found {
				return nil, fmt.Errorf("%s entry %d: expected version:base64", credentialPreviousKeysEnv, index+1)
			}
			if err := credentialcrypto.ValidateKeyVersion(entryVersion); err != nil {
				return nil, fmt.Errorf("%s entry %d: %w", credentialPreviousKeysEnv, index+1, err)
			}
			if _, duplicate := keys[entryVersion]; duplicate {
				return nil, fmt.Errorf("%s entry %d: key version %q is configured more than once", credentialPreviousKeysEnv, index+1, entryVersion)
			}
			key, err := credentialcrypto.ParseKey(entryKey)
			if err != nil {
				return nil, fmt.Errorf("%s entry %d: %w", credentialPreviousKeysEnv, index+1, err)
			}
			keys[entryVersion] = key
		}
	}

	ring, err := credentialcrypto.NewStaticKeyRing(version, keys)
	// The AES key schedules now hold the keys; clear the decoded copies
	// (best effort).
	for _, key := range keys {
		clear(key)
	}
	if err != nil {
		return nil, errors.Join(errors.New("credential key ring"), err)
	}
	return ring, nil
}

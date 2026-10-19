package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func encodedKey(t *testing.T, size int) string {
	t.Helper()
	key := make([]byte, size)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadCredentialKeyRing(t *testing.T) {
	active, previous := encodedKey(t, 32), encodedKey(t, 32)
	ring, err := loadCredentialKeyRing(env(map[string]string{
		credentialKeyEnv:          active,
		credentialKeyVersionEnv:   "2026-10",
		credentialPreviousKeysEnv: "2026-01:" + previous,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if ring.ActiveVersion() != "2026-10" || fmt.Sprint(ring.Versions()) != "[2026-01 2026-10]" {
		t.Fatalf("ring active=%s versions=%v", ring.ActiveVersion(), ring.Versions())
	}
}

func TestLoadCredentialKeyRingRejectsInvalidConfigurationWithoutEchoingKeys(t *testing.T) {
	valid := encodedKey(t, 32)
	short := encodedKey(t, 16)
	passphrase := "my-memorable-passphrase-SECRETWORD"
	cases := map[string]map[string]string{
		"missing key":              {credentialKeyVersionEnv: "v1"},
		"missing version":          {credentialKeyEnv: valid},
		"bad version":              {credentialKeyEnv: valid, credentialKeyVersionEnv: "v 1"},
		"passphrase key":           {credentialKeyEnv: passphrase, credentialKeyVersionEnv: "v1"},
		"short key":                {credentialKeyEnv: short, credentialKeyVersionEnv: "v1"},
		"hex key":                  {credentialKeyEnv: strings.Repeat("ab", 32), credentialKeyVersionEnv: "v1"},
		"previous without version": {credentialKeyEnv: valid, credentialKeyVersionEnv: "v1", credentialPreviousKeysEnv: short},
		"previous short key":       {credentialKeyEnv: valid, credentialKeyVersionEnv: "v1", credentialPreviousKeysEnv: "v0:" + short},
		"previous duplicates":      {credentialKeyEnv: valid, credentialKeyVersionEnv: "v1", credentialPreviousKeysEnv: "v1:" + valid},
	}
	for name, values := range cases {
		_, err := loadCredentialKeyRing(env(values))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		message := fmt.Sprintf("%v %+v", err, err)
		for _, secret := range []string{valid, short, passphrase, "SECRETWORD"} {
			if strings.Contains(message, secret) {
				t.Errorf("%s: error echoed key material: %s", name, message)
			}
		}
	}
}

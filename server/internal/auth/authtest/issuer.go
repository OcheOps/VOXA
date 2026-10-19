// Package authtest provides a local OIDC issuer (discovery + JWKS) and
// token signer so authentication tests never depend on an external provider.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const Audience = "voxa-test-client"

type signingKey struct {
	id  string
	key *rsa.PrivateKey
}

// Issuer is an in-process OIDC provider.
type Issuer struct {
	URL       string
	mu        sync.Mutex
	published []signingKey // served in JWKS
	active    signingKey   // signs new tokens
	down      atomic.Bool
	JWKSHits  atomic.Int64
}

func newKey(t testing.TB, id string) signingKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return signingKey{id: id, key: key}
}

func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	issuer := &Issuer{}
	issuer.active = newKey(t, "key-1")
	issuer.published = []signingKey{issuer.active}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	issuer.URL = server.URL
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer.URL,
			"jwks_uri":                              issuer.URL + "/jwks",
			"authorization_endpoint":                issuer.URL + "/authorize",
			"token_endpoint":                        issuer.URL + "/token",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		issuer.JWKSHits.Add(1)
		if issuer.down.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		issuer.mu.Lock()
		defer issuer.mu.Unlock()
		set := jose.JSONWebKeySet{}
		for _, k := range issuer.published {
			set.Keys = append(set.Keys, jose.JSONWebKey{Key: &k.key.PublicKey, KeyID: k.id, Algorithm: string(jose.RS256), Use: "sig"})
		}
		_ = json.NewEncoder(w).Encode(set)
	})
	return issuer
}

// SetDown makes the JWKS endpoint fail, simulating a provider outage.
func (i *Issuer) SetDown(down bool) { i.down.Store(down) }

// Rotate publishes a new signing key (keeping the old one published when
// keepOld is set) and signs future tokens with it.
func (i *Issuer) Rotate(t testing.TB, id string, keepOld bool) {
	t.Helper()
	next := newKey(t, id)
	i.mu.Lock()
	defer i.mu.Unlock()
	if keepOld {
		i.published = append(i.published, next)
	} else {
		i.published = []signingKey{next}
	}
	i.active = next
}

// Claims returns valid standard claims for subject, valid for an hour.
func (i *Issuer) Claims(subject string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": i.URL, "sub": subject, "aud": Audience,
		"iat": now.Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

// Token signs claims with the active key.
func (i *Issuer) Token(t testing.TB, claims map[string]any) string {
	t.Helper()
	i.mu.Lock()
	active := i.active
	i.mu.Unlock()
	return Sign(t, active.key, active.id, claims)
}

// TokenFor is a valid token for subject with optional extra claims.
func (i *Issuer) TokenFor(t testing.TB, subject string, extra map[string]any) string {
	claims := i.Claims(subject)
	for key, value := range extra {
		claims[key] = value
	}
	return i.Token(t, claims)
}

// Sign signs claims with an arbitrary RSA key (e.g. a forged one).
func Sign(t testing.TB, key *rsa.PrivateKey, keyID string, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID))
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// ForeignKey returns a fresh RSA key the issuer never published.
func ForeignKey(t testing.TB) *rsa.PrivateKey { return newKey(t, "foreign").key }

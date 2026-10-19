package auth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/auth"
	"github.com/OcheOps/voxa/server/internal/auth/authtest"
)

func newAuthenticator(t *testing.T, issuer *authtest.Issuer) *auth.OIDCAuthenticator {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authenticator, err := auth.NewOIDCAuthenticator(ctx, auth.OIDCConfig{Issuer: issuer.URL, Audience: authtest.Audience})
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}

func assertFailure(t *testing.T, err error, category string) {
	t.Helper()
	var failure *auth.Failure
	if !errors.As(err, &failure) || failure.Category != category {
		t.Fatalf("err = %v, want failure %s", err, category)
	}
}

func TestValidTokenAuthenticates(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	authenticator := newAuthenticator(t, issuer)
	token := issuer.TokenFor(t, "subject-1", map[string]any{"email": "ada@example.test", "name": "Ada"})
	identity, err := authenticator.Authenticate(context.Background(), "Bearer "+token)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Issuer != issuer.URL || identity.Subject != "subject-1" || *identity.Email != "ada@example.test" || *identity.DisplayName != "Ada" {
		t.Fatalf("identity = %#v", identity)
	}
	// Lower-case scheme is valid per RFC 6750.
	if _, err := authenticator.Authenticate(context.Background(), "bearer "+token); err != nil {
		t.Fatalf("lower-case scheme rejected: %v", err)
	}
}

func TestUnverifiedEmailIsIgnoredAndUsernameFallsBack(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	identity, err := newAuthenticator(t, issuer).Authenticate(context.Background(), "Bearer "+issuer.TokenFor(t, "s", map[string]any{
		"email": "spoof@example.test", "email_verified": false, "preferred_username": "ada",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if identity.Email != nil || identity.DisplayName == nil || *identity.DisplayName != "ada" {
		t.Fatalf("identity = %#v", identity)
	}
}

func TestRejectedTokens(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	authenticator := newAuthenticator(t, issuer)
	other := authtest.NewIssuer(t)
	now := time.Now()

	claims := func(overrides map[string]any) map[string]any {
		c := issuer.Claims("subject-1")
		for key, value := range overrides {
			if value == nil {
				delete(c, key)
			} else {
				c[key] = value
			}
		}
		return c
	}
	unsigned := func(c map[string]any) string {
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
		body := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iss":%q,"sub":"subject-1","aud":%q,"exp":%d}`, c["iss"], c["aud"], c["exp"])))
		return header + "." + body + "."
	}

	cases := []struct {
		name, header, category string
	}{
		{"missing header", "", auth.CategoryMissingBearer},
		{"basic scheme", "Basic dXNlcjpwYXNz", auth.CategoryMalformedHeader},
		{"bearer without token", "Bearer ", auth.CategoryMalformedHeader},
		{"token with spaces", "Bearer a b", auth.CategoryMalformedHeader},
		{"oversized token", "Bearer " + strings.Repeat("a", 17<<10), auth.CategoryMalformedHeader},
		{"garbage token", "Bearer not-a-jwt", auth.CategoryMalformedToken},
		{"unsigned alg none", "Bearer " + unsigned(claims(nil)), auth.CategoryMalformedToken},
		{"expired", "Bearer " + issuer.Token(t, claims(map[string]any{"exp": now.Add(-time.Minute).Unix()})), auth.CategoryExpired},
		{"not yet valid", "Bearer " + issuer.Token(t, claims(map[string]any{"nbf": now.Add(time.Hour).Unix()})), auth.CategoryNotYetValid},
		{"wrong issuer", "Bearer " + issuer.Token(t, claims(map[string]any{"iss": other.URL})), auth.CategoryIssuerMismatch},
		{"wrong audience", "Bearer " + issuer.Token(t, claims(map[string]any{"aud": "someone-else"})), auth.CategoryAudienceMismatch},
		{"forged signature, known kid", "Bearer " + authtest.Sign(t, authtest.ForeignKey(t), "key-1", claims(nil)), auth.CategorySignatureInvalid},
		{"token from another issuer", "Bearer " + other.Token(t, other.Claims("subject-1")), auth.CategorySignatureInvalid},
		{"missing subject", "Bearer " + issuer.Token(t, claims(map[string]any{"sub": nil})), auth.CategoryClaimsInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := authenticator.Authenticate(context.Background(), tc.header)
			assertFailure(t, err, tc.category)
			token := strings.TrimPrefix(tc.header, "Bearer ")
			if len(token) > 8 && strings.Contains(err.Error(), token) {
				t.Fatal("failure leaked token")
			}
		})
	}
}

func TestKeyRotationAndProviderOutage(t *testing.T) {
	issuer := authtest.NewIssuer(t)
	authenticator := newAuthenticator(t, issuer)
	ctx := context.Background()
	oldToken := issuer.TokenFor(t, "subject-1", nil)
	if _, err := authenticator.Authenticate(ctx, "Bearer "+oldToken); err != nil {
		t.Fatal(err)
	}
	hits := issuer.JWKSHits.Load()

	// Cached keys keep working without refetching, even while the provider
	// is down.
	issuer.SetDown(true)
	for range 5 {
		if _, err := authenticator.Authenticate(ctx, "Bearer "+oldToken); err != nil {
			t.Fatalf("cached key rejected during outage: %v", err)
		}
	}
	if issuer.JWKSHits.Load() != hits {
		t.Fatal("JWKS fetched on every request")
	}

	// A token signed by a new key cannot be verified while JWKS is down:
	// that is an availability failure, not an invalid token.
	issuer.Rotate(t, "key-2", true)
	_, err := authenticator.Authenticate(ctx, "Bearer "+issuer.TokenFor(t, "subject-1", nil))
	var failure *auth.Failure
	if !errors.As(err, &failure) || failure.Category != auth.CategoryKeysUnavailable || !failure.Unavailable {
		t.Fatalf("err = %v, want signing_keys_unavailable", err)
	}

	// Once the provider recovers, the rotated key is fetched and accepted.
	issuer.SetDown(false)
	if _, err := authenticator.Authenticate(ctx, "Bearer "+issuer.TokenFor(t, "subject-1", nil)); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
}

func TestOIDCConfigurationValidation(t *testing.T) {
	for _, issuer := range []string{"", "issuer", "http://idp.example.com", "https://user:pw@idp.example.com", "https://idp.example.com?x=1", "ftp://idp.example.com"} {
		if err := auth.ValidateIssuerURL(issuer); err == nil {
			t.Errorf("issuer %q accepted", issuer)
		}
	}
	for _, issuer := range []string{"https://idp.example.com/realms/voxa", "http://localhost:8081", "http://127.0.0.1:9000/x"} {
		if err := auth.ValidateIssuerURL(issuer); err != nil {
			t.Errorf("issuer %q rejected: %v", issuer, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := auth.NewOIDCAuthenticator(ctx, auth.OIDCConfig{Issuer: authtest.NewIssuer(t).URL}); err == nil {
		t.Error("missing audience accepted")
	}
	if _, err := auth.NewOIDCAuthenticator(ctx, auth.OIDCConfig{Issuer: "http://127.0.0.1:1", Audience: "a"}); err == nil {
		t.Error("unreachable issuer accepted")
	}
	// Discovery must report exactly the configured issuer.
	issuer := authtest.NewIssuer(t)
	if _, err := auth.NewOIDCAuthenticator(ctx, auth.OIDCConfig{Issuer: issuer.URL + "/", Audience: "a"}); err == nil {
		t.Error("issuer mismatch with discovery accepted")
	}
}

func TestDevelopmentAuthenticatorIsFixed(t *testing.T) {
	identity, err := auth.DevelopmentAuthenticator{}.Authenticate(context.Background(), "")
	if err != nil || identity.Issuer != "voxa-development" || identity.Subject != "developer" {
		t.Fatalf("identity=%#v err=%v", identity, err)
	}
}

// Package auth authenticates HTTP requests into a domain.Identity. It knows
// about bearer tokens and OIDC; nothing outside it sees tokens or claims.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/OcheOps/voxa/server/internal/domain"
)

// Authenticator turns the raw Authorization header value into a verified
// identity, or a *Failure.
type Authenticator interface {
	Authenticate(ctx context.Context, authorization string) (domain.Identity, error)
}

// Failure is a safe authentication outcome. Category is a fixed vocabulary
// for logs; it never contains token material, claims or library errors.
type Failure struct {
	Category string
	// Unavailable marks failures caused by the identity provider being
	// unreachable (e.g. signing keys could not be fetched), not the token.
	Unavailable bool
}

func (f *Failure) Error() string { return "authentication failed: " + f.Category }

const (
	CategoryMissingBearer     = "missing_bearer"
	CategoryMalformedHeader   = "malformed_authorization"
	CategoryMalformedToken    = "malformed_token"
	CategoryExpired           = "expired"
	CategoryNotYetValid       = "not_yet_valid"
	CategoryIssuerMismatch    = "issuer_mismatch"
	CategoryAudienceMismatch  = "audience_mismatch"
	CategorySignatureInvalid  = "signature_invalid"
	CategoryKeysUnavailable   = "signing_keys_unavailable"
	CategoryClaimsInvalid     = "claims_invalid"
	CategoryVerificationError = "verification_failed"
)

// maxTokenLength bounds work done on attacker-supplied headers.
const maxTokenLength = 16 << 10

// BearerToken extracts the token from "Bearer <token>" (scheme is
// case-insensitive per RFC 6750).
func BearerToken(authorization string) (string, error) {
	if authorization == "" {
		return "", &Failure{Category: CategoryMissingBearer}
	}
	scheme, token, found := strings.Cut(authorization, " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || len(token) > maxTokenLength || strings.ContainsAny(token, " \t") {
		return "", &Failure{Category: CategoryMalformedHeader}
	}
	return token, nil
}

// OIDCConfig configures generic OpenID Connect bearer-token validation.
type OIDCConfig struct {
	// Issuer must exactly match the provider's discovery "issuer" and every
	// token's "iss" claim.
	Issuer string
	// Audience must appear in every token's "aud" claim (the Voxa client ID
	// for ID tokens, or the API audience for JWT access tokens).
	Audience string
	// HTTPClient is used for discovery and JWKS; defaults to a client with
	// a 10s timeout.
	HTTPClient *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
}

// ValidateIssuerURL requires https, except plain http on loopback hosts for
// local development and tests.
func ValidateIssuerURL(issuer string) error {
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("OIDC issuer must be an absolute URL without credentials, query or fragment")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return errors.New("OIDC issuer must use https (http is allowed only for loopback hosts)")
}

// OIDCAuthenticator validates JWT bearer tokens issued by one OIDC provider:
// signature (keys from the provider's discovery/JWKS, cached and refreshed
// on rotation), issuer, audience, expiry and not-before. Unsigned tokens and
// the "none" algorithm are never accepted.
type OIDCAuthenticator struct {
	verifier *oidc.IDTokenVerifier
	issuer   string
}

// NewOIDCAuthenticator performs discovery once (bounded by ctx). Failure is
// a startup error.
func NewOIDCAuthenticator(ctx context.Context, config OIDCConfig) (*OIDCAuthenticator, error) {
	if err := ValidateIssuerURL(config.Issuer); err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.Audience) == "" {
		return nil, errors.New("OIDC audience is required")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed: %w", err)
	}
	// The key set lives for the process; its context must outlive startup.
	keyContext := oidc.ClientContext(context.Background(), client)
	verifier := provider.VerifierContext(keyContext, &oidc.Config{ClientID: config.Audience, Now: config.Now})
	return &OIDCAuthenticator{verifier: verifier, issuer: config.Issuer}, nil
}

type profileClaims struct {
	Email             string `json:"email"`
	EmailVerified     *bool  `json:"email_verified"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
}

func (a *OIDCAuthenticator) Authenticate(ctx context.Context, authorization string) (domain.Identity, error) {
	token, err := BearerToken(authorization)
	if err != nil {
		return domain.Identity{}, err
	}
	verified, err := a.verifier.Verify(ctx, token)
	if err != nil {
		return domain.Identity{}, classify(err)
	}
	// Claims are read only after signature, issuer, audience and time
	// validation succeeded.
	if verified.Subject == "" || verified.Issuer != a.issuer {
		return domain.Identity{}, &Failure{Category: CategoryClaimsInvalid}
	}
	var claims profileClaims
	if err := verified.Claims(&claims); err != nil {
		return domain.Identity{}, &Failure{Category: CategoryClaimsInvalid}
	}
	identity := domain.Identity{Issuer: verified.Issuer, Subject: verified.Subject}
	if claims.Email != "" && (claims.EmailVerified == nil || *claims.EmailVerified) {
		identity.Email = &claims.Email
	}
	if claims.Name != "" {
		identity.DisplayName = &claims.Name
	} else if claims.PreferredUsername != "" {
		identity.DisplayName = &claims.PreferredUsername
	}
	return identity, nil
}

// classify maps go-oidc verification errors to safe categories. go-oidc
// verifies the signature first and only then checks issuer, audience and
// time claims, so the category names the first failing check. Categories
// are for logs only; the underlying error is discarded because it can echo
// claim values.
func classify(err error) *Failure {
	var expired *oidc.TokenExpiredError
	if errors.As(err, &expired) {
		return &Failure{Category: CategoryExpired}
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "issued by a different provider"):
		return &Failure{Category: CategoryIssuerMismatch}
	case strings.Contains(message, "expected audience"):
		return &Failure{Category: CategoryAudienceMismatch}
	case strings.Contains(message, "(not before)"):
		return &Failure{Category: CategoryNotYetValid}
	case strings.Contains(message, "fetching keys"):
		return &Failure{Category: CategoryKeysUnavailable, Unavailable: true}
	case strings.Contains(message, "failed to verify signature"):
		return &Failure{Category: CategorySignatureInvalid}
	case strings.Contains(message, "malformed jwt"), strings.Contains(message, "not signed"),
		strings.Contains(message, "multiple signatures"), strings.Contains(message, "unmarshal claims"):
		return &Failure{Category: CategoryMalformedToken}
	}
	return &Failure{Category: CategoryVerificationError}
}

// DevelopmentAuthenticator accepts every request as one fixed identity. It
// must only be constructed for VOXA_AUTH_MODE=development.
type DevelopmentAuthenticator struct{}

func (DevelopmentAuthenticator) Authenticate(context.Context, string) (domain.Identity, error) {
	return domain.Identity{Issuer: "voxa-development", Subject: "developer"}, nil
}

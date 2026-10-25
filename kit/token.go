// Package iimakit is the platform kit for Go apps behind the students.iima.ac.in gateway.
//
// The gateway (Traefik plus core's /auth/verify) puts a short-lived signed JWT in the
// X-User-Token header of every request from a signed-in user the app's policy allows.
// This package verifies that token with core's public keys and hands the app the user.
// It has no PocketBase dependency; iimakit/pb builds on it.
package iimakit

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// TokenHeader is the request header the gateway sets. Browsers can never set it: the gateway
// strips it from every incoming request first.
const TokenHeader = "X-User-Token"

// DefaultIssuer is the iss claim core puts in every token.
const DefaultIssuer = "students.iima.ac.in"

// ClaimsVersion is the v claim this kit understands. A breaking change to the claims bumps it.
const ClaimsVersion = 1

// Claims is the payload of the identity token (version 1). Core mints it; apps read it with User(r).
type Claims struct {
	V        int    `json:"v"`
	Issuer   string `json:"iss"`
	Audience string `json:"aud"` // the app id; a token for one app is rejected by every other app
	Subject  string `json:"sub"` // stable user id, e.g. "u_8f3k..."
	Session  string `json:"sid"` // changes on every new sign-in
	IssuedAt int64  `json:"iat"`
	Expiry   int64  `json:"exp"`

	Email       string `json:"email"`
	Name        string `json:"name,omitempty"`
	Picture     string `json:"picture,omitempty"`
	Provider    string `json:"provider"`              // "google", or "dev" on the local stack
	Member      bool   `json:"member"`                // signed in with an @iima.ac.in Google account
	Affiliation string `json:"affiliation,omitempty"` // student, faculty, staff, alum, external
	Programme   string `json:"programme,omitempty"`   // PGP, PGPX, PhD, ePGP...
	Batch       int    `json:"batch,omitempty"`
	Year        int    `json:"year,omitempty"`

	Roles []string `json:"roles,omitempty"` // only this app's roles
}

// HasRole reports whether the user has the given role in this app.
func (c *Claims) HasRole(role string) bool {
	if c == nil {
		return false
	}
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Errors returned by Verify. Callers should treat any error as "signed out".
var (
	ErrMalformed     = errors.New("iimakit: malformed token")
	ErrUnknownKey    = errors.New("iimakit: token signed with an unknown key")
	ErrBadSignature  = errors.New("iimakit: bad token signature")
	ErrWrongAudience = errors.New("iimakit: token is for another app")
	ErrWrongIssuer   = errors.New("iimakit: token has the wrong issuer")
	ErrExpired       = errors.New("iimakit: token expired")
	ErrNotYetValid   = errors.New("iimakit: token issued in the future")
	ErrVersion       = errors.New("iimakit: unsupported token version")
)

// Verifier checks identity tokens for one app. Public keys come from core's JWKS endpoint,
// are cached, and are fetched again when a token names a key id we have not seen (key rotation).
type Verifier struct {
	AppID   string
	Issuer  string
	JWKSURL string
	Client  *http.Client
	// Leeway allows for small clock differences between servers.
	Leeway time.Duration

	now func() time.Time

	mu        sync.Mutex
	keys      map[string]ed25519.PublicKey
	fetchedAt time.Time
}

// Refetch limits: at most one fetch per minRefetch when an unknown kid shows up (so a flood of
// forged tokens cannot hammer core), and a background refresh once keys are older than maxKeyAge.
const (
	minRefetch = 10 * time.Second
	maxKeyAge  = time.Hour
)

// NewVerifier returns a Verifier for appID, configured from the environment:
// IIMA_JWKS_URL (default: the local stack's http://localhost:8080/auth/.well-known/jwks.json)
// and IIMA_ISSUER (default students.iima.ac.in).
func NewVerifier(appID string) *Verifier {
	return &Verifier{
		AppID:   appID,
		Issuer:  envOr("IIMA_ISSUER", DefaultIssuer),
		JWKSURL: envOr("IIMA_JWKS_URL", "http://localhost:8080/auth/.well-known/jwks.json"),
		Client:  &http.Client{Timeout: 5 * time.Second},
		Leeway:  30 * time.Second,
	}
}

// Verify checks the token's signature, issuer, audience, version and expiry, and returns its claims.
func (v *Verifier) Verify(ctx context.Context, raw string) (*Claims, error) {
	if v.AppID == "" {
		return nil, errors.New("iimakit: Verifier has no AppID")
	}
	// Only EdDSA is accepted. This rules out "none" and HS256-with-the-public-key tricks.
	jws, err := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil || len(jws.Signatures) != 1 {
		return nil, ErrMalformed
	}
	kid := jws.Signatures[0].Header.KeyID
	if kid == "" {
		return nil, ErrMalformed
	}
	key, err := v.key(ctx, kid)
	if err != nil {
		return nil, err
	}
	payload, err := jws.Verify(key)
	if err != nil {
		return nil, ErrBadSignature
	}

	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		// Includes an aud given as an array: core only ever mints a single string.
		return nil, ErrMalformed
	}
	if c.V != ClaimsVersion {
		return nil, ErrVersion
	}
	if c.Issuer != v.Issuer {
		return nil, ErrWrongIssuer
	}
	if c.Audience != v.AppID {
		return nil, ErrWrongAudience
	}
	if c.Subject == "" || c.Email == "" {
		return nil, ErrMalformed
	}
	now := v.clock()
	if c.Expiry == 0 || now.After(time.Unix(c.Expiry, 0).Add(v.Leeway)) {
		return nil, ErrExpired
	}
	if time.Unix(c.IssuedAt, 0).After(now.Add(v.Leeway)) {
		return nil, ErrNotYetValid
	}
	return &c, nil
}

func (v *Verifier) clock() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now()
}

// key returns the public key for kid, fetching the JWKS if needed.
func (v *Verifier) key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	k, ok := v.keys[kid]
	age := v.clock().Sub(v.fetchedAt)
	switch {
	case ok && age < maxKeyAge:
		return k, nil
	case ok:
		// Known key but stale set: refresh, and keep using the old set if core is unreachable.
		_ = v.fetchLocked(ctx)
	case age >= minRefetch:
		if err := v.fetchLocked(ctx); err != nil {
			return nil, err
		}
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, ErrUnknownKey
}

func (v *Verifier) fetchLocked(ctx context.Context) error {
	// Count failed attempts too, so a down core is not retried on every request.
	v.fetchedAt = v.clock()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.JWKSURL, nil)
	if err != nil {
		return err
	}
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("iimakit: fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("iimakit: fetch JWKS: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("iimakit: fetch JWKS: %w", err)
	}
	keys, err := ParseJWKS(body)
	if err != nil {
		return err
	}
	v.keys = keys
	return nil
}

// ParseJWKS reads a JWKS document and returns its Ed25519 signing keys by key id.
// Keys of any other type are ignored.
func ParseJWKS(body []byte) (map[string]ed25519.PublicKey, error) {
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("iimakit: parse JWKS: %w", err)
	}
	keys := make(map[string]ed25519.PublicKey)
	for _, k := range set.Keys {
		pub, ok := k.Key.(ed25519.PublicKey)
		if !ok || k.KeyID == "" || (k.Use != "" && k.Use != "sig") || (k.Algorithm != "" && k.Algorithm != string(jose.EdDSA)) {
			continue
		}
		keys[k.KeyID] = pub
	}
	return keys, nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Package token mints the X-User-Token JWTs and publishes the matching public keys (JWKS).
package token

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/arnav127/iima-platform/core/store"
	iimakit "github.com/arnav127/iima-platform/kit"
)

// Signer signs with the newest key and publishes every stored key, so tokens signed just before a
// rotation still verify.
type Signer struct {
	Issuer string
	TTL    time.Duration

	signer jose.Signer
	jwks   []byte
}

// Load reads the signing keys, generating the first one if there are none.
func Load(ctx context.Context, st *store.Store, issuer string, ttl time.Duration) (*Signer, error) {
	keys, err := st.SigningKeys(ctx)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		k, err := NewKey()
		if err != nil {
			return nil, err
		}
		if err := st.AddSigningKey(ctx, k); err != nil {
			return nil, err
		}
		keys = []store.SigningKey{k}
	}
	return New(keys, issuer, ttl)
}

// NewKey generates a fresh Ed25519 key. Its kid is derived from the public key.
func NewKey() (store.SigningKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return store.SigningKey{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return store.SigningKey{}, err
	}
	sum := sha256.Sum256(pub)
	return store.SigningKey{KID: base64.RawURLEncoding.EncodeToString(sum[:12]), PrivateKey: der, CreatedAt: time.Now()}, nil
}

// New builds a Signer from keys, newest first.
func New(keys []store.SigningKey, issuer string, ttl time.Duration) (*Signer, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("token: no signing keys")
	}
	var set jose.JSONWebKeySet
	var newest ed25519.PrivateKey
	for i, k := range keys {
		parsed, err := x509.ParsePKCS8PrivateKey(k.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("token: key %s: %w", k.KID, err)
		}
		priv, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("token: key %s is not Ed25519", k.KID)
		}
		if i == 0 {
			newest = priv
		}
		set.Keys = append(set.Keys, jose.JSONWebKey{Key: priv.Public(), KeyID: k.KID, Algorithm: string(jose.EdDSA), Use: "sig"})
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: jose.JSONWebKey{Key: newest, KeyID: keys[0].KID}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return nil, err
	}
	jwks, err := json.Marshal(set)
	if err != nil {
		return nil, err
	}
	return &Signer{Issuer: issuer, TTL: ttl, signer: signer, jwks: jwks}, nil
}

// Mint fills in v, iss, iat and exp and signs the claims. The caller sets aud and the user fields.
func (s *Signer) Mint(c iimakit.Claims) (string, error) {
	now := time.Now()
	c.V = iimakit.ClaimsVersion
	c.Issuer = s.Issuer
	c.IssuedAt = now.Unix()
	c.Expiry = now.Add(s.TTL).Unix()
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	jws, err := s.signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

// JWKS is the public key set, as served at /auth/.well-known/jwks.json.
func (s *Signer) JWKS() []byte { return s.jwks }

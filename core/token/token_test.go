package token

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/arnav127/iima-platform/core/store"
	iimakit "github.com/arnav127/iima-platform/kit"
)

// Rotation: core signs with the newest key and keeps publishing the older one, so apps accept
// tokens from both sides of a restart.
func TestRotation(t *testing.T) {
	old, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	next, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	next.CreatedAt = old.CreatedAt.Add(time.Hour)

	before, err := New([]store.SigningKey{old}, "students.iima.ac.in", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	after, err := New([]store.SigningKey{next, old}, "students.iima.ac.in", time.Minute) // newest first, as the store returns them
	if err != nil {
		t.Fatal(err)
	}
	claims := iimakit.Claims{Audience: "demo", Subject: "u_1", Email: "p25me@iima.ac.in", Provider: "google"}
	oldTok, _ := before.Mint(claims)
	newTok, _ := after.Mint(claims)

	kid := func(tok string) string {
		jws, err := jose.ParseSigned(tok, []jose.SignatureAlgorithm{jose.EdDSA})
		if err != nil {
			t.Fatal(err)
		}
		return jws.Signatures[0].Header.KeyID
	}
	if kid(newTok) != next.KID || kid(oldTok) != old.KID {
		t.Fatal("core must sign with its newest key")
	}

	keys, err := iimakit.ParseJWKS(after.JWKS())
	if err != nil || len(keys) != 2 || keys[old.KID] == nil || keys[next.KID] == nil {
		t.Fatalf("JWKS after rotation must publish both keys: %v %v", keys, err)
	}
	for _, tok := range []string{oldTok, newTok} {
		jws, _ := jose.ParseSigned(tok, []jose.SignatureAlgorithm{jose.EdDSA})
		if _, err := jws.Verify(keys[jws.Signatures[0].Header.KeyID]); err != nil {
			t.Fatal("a token from before or after the rotation does not verify")
		}
	}
	var set struct{ Keys []map[string]any }
	if json.Unmarshal(after.JWKS(), &set) != nil || len(set.Keys) != 2 {
		t.Fatal("bad JWKS")
	}
	for _, k := range set.Keys {
		if _, private := k["d"]; private {
			t.Fatal("the JWKS leaks a private key")
		}
	}
}

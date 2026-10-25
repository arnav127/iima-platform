package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/arnav127/iima-platform/core/store"
)

// Identity is what a provider tells us about the person who signed in.
type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
	HostedDomain  string // Google's hd claim: the Workspace domain, empty for Gmail accounts
}

// AuthOptions tune the provider's sign-in page.
type AuthOptions struct {
	HostedDomain  string // hint to show only accounts of this domain; never trusted on its own
	SelectAccount bool   // always show the account chooser (Switch account)
}

// Provider is an OIDC sign-in provider. Google is the only one for now; tests use a fake.
type Provider interface {
	AuthCodeURL(ls store.LoginState, opt AuthOptions) string
	Exchange(ctx context.Context, code string, ls *store.LoginState) (*Identity, error)
}

type Google struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewGoogle discovers Google's OIDC endpoints. redirectURL is PublicURL + /auth/callback and must
// be listed in the OAuth client in Google Cloud Console.
func NewGoogle(ctx context.Context, clientID, clientSecret, redirectURL string) (*Google, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return nil, fmt.Errorf("google oidc discovery: %w", err)
	}
	return &Google{
		oauth: &oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
			Endpoint: p.Endpoint(), Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verifier: p.Verifier(&oidc.Config{ClientID: clientID}),
	}, nil
}

func (g *Google) AuthCodeURL(ls store.LoginState, opt AuthOptions) string {
	opts := []oauth2.AuthCodeOption{oidc.Nonce(ls.Nonce), oauth2.S256ChallengeOption(ls.Verifier)}
	if opt.HostedDomain != "" {
		opts = append(opts, oauth2.SetAuthURLParam("hd", opt.HostedDomain))
	}
	if opt.SelectAccount {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "select_account"))
	}
	return g.oauth.AuthCodeURL(ls.State, opts...)
}

func (g *Google) Exchange(ctx context.Context, code string, ls *store.LoginState) (*Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tok, err := g.oauth.Exchange(ctx, code, oauth2.VerifierOption(ls.Verifier))
	if err != nil {
		return nil, fmt.Errorf("code exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("no id_token in response")
	}
	// Checks signature, issuer, audience (our client id) and expiry.
	idt, err := g.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("id token: %w", err)
	}
	if idt.Nonce != ls.Nonce {
		return nil, errors.New("id token: nonce mismatch")
	}
	var c struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
		HD            string `json:"hd"`
	}
	if err := idt.Claims(&c); err != nil {
		return nil, fmt.Errorf("id token claims: %w", err)
	}
	return &Identity{Subject: idt.Subject, Email: c.Email, EmailVerified: c.EmailVerified, Name: c.Name, Picture: c.Picture, HostedDomain: c.HD}, nil
}

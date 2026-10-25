package server

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/arnav127/iima-platform/core/attrs"
	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
	iimakit "github.com/arnav127/iima-platform/kit"
)

// identityCache keeps each person's roles (per app) and attribute override for cacheTTL, because
// they are needed on every request. Changes made through this core clear it at once; with several
// core instances, a change reaches the others within cacheTTL.
type identityCache struct {
	mu sync.Mutex
	m  map[string]identityEntry // "app|email" for roles, "|email" for the override
}

type identityEntry struct {
	roles    []string
	override *store.Override
	at       time.Time
}

func (c *identityCache) get(key string) (identityEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Since(e.at) > cacheTTL {
		return identityEntry{}, false
	}
	return e, true
}

func (c *identityCache) put(key string, e identityEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 50_000 {
		clear(c.m)
	}
	e.at = time.Now()
	c.m[key] = e
}

// forget drops everything cached about email (all apps).
func (c *identityCache) forget(email string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if strings.HasSuffix(k, "|"+email) {
			delete(c.m, k)
		}
	}
}

func (s *Server) roles(ctx context.Context, app, email string) ([]string, error) {
	key := app + "|" + email
	if e, ok := s.ids.get(key); ok {
		return e.roles, nil
	}
	r, err := s.cfg.Store.Roles(ctx, app, email)
	if err != nil {
		return nil, err
	}
	s.ids.put(key, identityEntry{roles: r})
	return r, nil
}

func (s *Server) attributes(ctx context.Context, u store.User) (attrs.Attributes, error) {
	key := "|" + u.Email
	e, ok := s.ids.get(key)
	if !ok {
		o, err := s.cfg.Store.AttributeOverride(ctx, u.Email)
		if err != nil {
			return attrs.Attributes{}, err
		}
		e = identityEntry{override: o}
		s.ids.put(key, e)
	}
	return attrs.Derive(u.Email, u.Member, e.override, time.Now()), nil
}

// claimsFor builds the token payload for one app: who the user is, their attributes, and only
// this app's roles.
func (s *Server) claimsFor(ctx context.Context, sess *store.Session, app *registry.App) (iimakit.Claims, error) {
	u := sess.User
	a, err := s.attributes(ctx, u)
	if err != nil {
		return iimakit.Claims{}, err
	}
	roles, err := s.roles(ctx, app.ID, u.Email)
	if err != nil {
		return iimakit.Claims{}, err
	}
	return iimakit.Claims{
		Audience: app.ID, Subject: u.ID, Session: sess.SID,
		Email: u.Email, Name: u.Name, Picture: u.Picture, Provider: u.Provider, Member: u.Member,
		Affiliation: a.Affiliation, Programme: a.Programme, Batch: a.Batch, Year: a.Year,
		Roles: roles,
	}, nil
}

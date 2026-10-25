package server

import (
	"net/http"
	"strings"

	iimakit "github.com/arnav127/iima-platform/kit"
)

// handleVerify is the gateway's forward-auth endpoint, called before every app request with the
// browser's headers plus X-Forwarded-Uri and X-Forwarded-Method.
//
//	mode=optional: always 200; adds X-User-Token when signed in and the app's policy allows the user.
//	mode=required: 200 with the token, else a redirect (browsers) or 401/403 (APIs), which the
//	               gateway returns to the client instead of calling the app.
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	if mode != "optional" && mode != "required" {
		// A misconfigured middleware. Fail closed rather than guess.
		s.cfg.Log.Error("verify called without a valid mode", "mode", mode)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "gateway misconfigured"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	uri := r.Header.Get("X-Forwarded-Uri")
	app := s.cfg.Registry.Lookup(uri)
	sess := s.session(r)

	switch {
	case app != nil && sess != nil && allowed(sess.User, app):
		u := sess.User
		tok, err := s.cfg.Signer.Mint(iimakit.Claims{
			Audience: app.ID, Subject: u.ID, Session: sess.SID,
			Email: u.Email, Name: u.Name, Picture: u.Picture, Provider: u.Provider, Member: u.Member,
		})
		if err != nil {
			s.serverError(w, "mint token", err)
			return
		}
		w.Header().Set(iimakit.TokenHeader, tok)
		w.WriteHeader(http.StatusOK)
	case mode == "optional":
		w.WriteHeader(http.StatusOK)
	case app == nil:
		// auth-required on a path that is not in the registry: nothing to allow.
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "unknown app"})
	case sess == nil:
		s.refuse(w, r, http.StatusUnauthorized, loginURL(safeOr(uri, app.Path)))
	default:
		s.refuse(w, r, http.StatusForbidden, deniedURL(safeOr(uri, app.Path)))
	}
}

// refuse redirects browser page loads to target and gives everything else a JSON status.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, status int, target string) {
	method := r.Header.Get("X-Forwarded-Method")
	if (method == "" || method == http.MethodGet) && strings.Contains(r.Header.Get("Accept"), "text/html") {
		// Absolute, because Traefik resolves a relative Location against core's internal address.
		w.Header().Set("Location", s.cfg.PublicURL.Scheme+"://"+s.cfg.PublicURL.Host+target)
		w.WriteHeader(http.StatusFound)
		return
	}
	msg := "sign in required"
	if status == http.StatusForbidden {
		msg = "this app needs your @" + s.cfg.MemberDomain + " account"
	}
	writeJSON(w, status, map[string]string{"error": msg, "login": target})
}

// handleMe is the current user for frontends. /auth/me?app=<id> also says whether that app's
// policy allows them.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := s.session(r)
	if sess == nil {
		writeJSON(w, http.StatusOK, map[string]any{"signedIn": false})
		return
	}
	u := sess.User
	out := map[string]any{
		"signedIn": true, "email": u.Email, "name": u.Name, "picture": u.Picture,
		"provider": u.Provider, "member": u.Member,
	}
	if id := r.URL.Query().Get("app"); id != "" {
		if a := s.cfg.Registry.ByID(id); a != nil {
			out["allowed"] = allowed(u, a)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleJWKS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(s.cfg.Signer.JWKS())
}

package server

import (
	"crypto/subtle"
	"net/http"
	"net/mail"
	"strings"

	"golang.org/x/oauth2"

	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
)

// handleLogin starts a sign-in: /auth/login?rd=/app/path[&switch=1].
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rd := safeOr(q.Get("rd"), "/")
	switchAccount := q.Get("switch") == "1"
	app := s.cfg.Registry.Lookup(rd)

	// Already signed in: go straight back, or to the "needs your IIMA account" page.
	if sess := s.session(r); sess != nil && !switchAccount {
		if app == nil || allowed(sess.User, app) {
			http.Redirect(w, r, rd, http.StatusFound)
		} else {
			http.Redirect(w, r, deniedURL(rd), http.StatusFound)
		}
		return
	}

	ls := store.LoginState{
		State:    store.RandomID("", 24),
		Nonce:    store.RandomID("", 24),
		Verifier: oauth2.GenerateVerifier(),
		RD:       rd,
	}
	if err := s.cfg.Store.CreateLoginState(r.Context(), ls); err != nil {
		s.serverError(w, "create login state", err)
		return
	}
	// Binds the callback to this browser, so nobody can sign a victim into the attacker's account
	// by sending them a callback link.
	s.setCookie(w, loginCookie, ls.State, "/auth/", loginTTL)
	w.Header().Set("Cache-Control", "no-store")

	if s.cfg.DevLogin && q.Get("provider") != "google" {
		s.render(w, http.StatusOK, "login", pageData{
			Title: "Sign in", App: app, State: ls.State, RD: rd, Switch: switchAccount,
			Google: s.cfg.Provider != nil, MemberDomain: s.cfg.MemberDomain,
		})
		return
	}
	if s.cfg.Provider == nil {
		s.render(w, http.StatusServiceUnavailable, "error", pageData{Title: "Sign-in unavailable", Message: "Google sign-in is not configured on this server."})
		return
	}
	opt := AuthOptions{SelectAccount: switchAccount}
	if app == nil || app.Login == registry.LoginIIMA {
		opt.HostedDomain = s.cfg.MemberDomain
	}
	http.Redirect(w, r, s.cfg.Provider.AuthCodeURL(ls, opt), http.StatusFound)
}

// takeLoginState checks the state against the browser's login cookie and consumes it.
func (s *Server) takeLoginState(w http.ResponseWriter, r *http.Request, state string) *store.LoginState {
	ck, err := r.Cookie(loginCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(ck.Value), []byte(state)) != 1 {
		s.render(w, http.StatusBadRequest, "error", pageData{Title: "Sign-in expired", Message: "This sign-in link is not valid in this browser. Please start again.", RD: "/"})
		return nil
	}
	s.clearCookie(w, loginCookie, "/auth/")
	ls, err := s.cfg.Store.TakeLoginState(r.Context(), state, loginTTL)
	if err != nil {
		s.render(w, http.StatusBadRequest, "error", pageData{Title: "Sign-in expired", Message: "That took too long. Please start again.", RD: "/"})
		return nil
	}
	return ls
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Provider == nil {
		s.handleNotFound(w, r)
		return
	}
	q := r.URL.Query()
	ls := s.takeLoginState(w, r, q.Get("state"))
	if ls == nil {
		return
	}
	if e := q.Get("error"); e != "" {
		s.render(w, http.StatusOK, "error", pageData{Title: "Sign-in cancelled", Message: "Google did not sign you in (" + e + ").", RD: ls.RD, Retry: loginURL(ls.RD)})
		return
	}
	id, err := s.cfg.Provider.Exchange(r.Context(), q.Get("code"), ls)
	if err != nil {
		s.cfg.Log.Warn("google sign-in failed", "err", err)
		s.render(w, http.StatusBadGateway, "error", pageData{Title: "Sign-in failed", Message: "Google sign-in did not complete. Please try again.", Retry: loginURL(ls.RD)})
		return
	}
	if !id.EmailVerified || id.Email == "" {
		s.render(w, http.StatusForbidden, "error", pageData{Title: "Email not verified", Message: "Your Google account's email address is not verified.", Retry: loginURL(ls.RD) + "&switch=1"})
		return
	}
	u, err := s.cfg.Store.UpsertUser(r.Context(), "google", id.Subject, store.User{
		Email: strings.ToLower(id.Email), Name: id.Name, Picture: id.Picture,
		Member: s.isMember(id.Email, id.HostedDomain),
	})
	if err != nil {
		s.serverError(w, "upsert user", err)
		return
	}
	s.finishLogin(w, r, u, ls.RD)
}

// handleDevLogin signs in as any email, for the local stack. Off unless DevLogin is set.
func (s *Server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.DevLogin {
		s.handleNotFound(w, r)
		return
	}
	if !s.sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	ls := s.takeLoginState(w, r, r.PostFormValue("state"))
	if ls == nil {
		return
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(r.PostFormValue("email")))
	if err != nil || len(addr.Address) > 254 {
		s.render(w, http.StatusBadRequest, "error", pageData{Title: "Bad email", Message: "Enter an email address like p25someone@iima.ac.in.", Retry: loginURL(ls.RD)})
		return
	}
	email := strings.ToLower(addr.Address)
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		name, _, _ = strings.Cut(email, "@")
	}
	// Dev accounts have no hd claim, so the email domain stands in for it.
	u, err := s.cfg.Store.UpsertUser(r.Context(), "dev", email, store.User{
		Email: email, Name: name, Member: strings.HasSuffix(email, "@"+strings.ToLower(s.cfg.MemberDomain)),
	})
	if err != nil {
		s.serverError(w, "upsert user", err)
		return
	}
	s.finishLogin(w, r, u, ls.RD)
}

// finishLogin replaces any old session with a new one and sends the browser on.
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, u *store.User, rd string) {
	if ck, err := r.Cookie(sessionCookie); err == nil {
		s.cache.drop(ck.Value)
		_ = s.cfg.Store.DeleteSession(r.Context(), ck.Value)
	}
	tok, sid, err := s.cfg.Store.CreateSession(r.Context(), u.ID, s.cfg.SessionTTL)
	if err != nil {
		s.serverError(w, "create session", err)
		return
	}
	s.setCookie(w, sessionCookie, tok, "/", s.cfg.SessionTTL)
	s.cfg.Log.Info("signed in", "user", u.ID, "provider", u.Provider, "member", u.Member, "sid", sid)

	if app := s.cfg.Registry.Lookup(rd); app != nil && !allowed(*u, app) {
		http.Redirect(w, r, deniedURL(rd), http.StatusFound)
		return
	}
	http.Redirect(w, r, rd, http.StatusFound)
}

// handleLogoutPage shows a button: logout needs a POST so other pages cannot sign people out.
func (s *Server) handleLogoutPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "logout", pageData{Title: "Sign out", Session: s.session(r), RD: safeOr(r.URL.Query().Get("rd"), "/")})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	if ck, err := r.Cookie(sessionCookie); err == nil {
		s.cache.drop(ck.Value)
		if err := s.cfg.Store.DeleteSession(r.Context(), ck.Value); err != nil {
			s.serverError(w, "delete session", err)
			return
		}
	}
	s.clearCookie(w, sessionCookie, "/")
	http.Redirect(w, r, safeOr(r.PostFormValue("rd"), "/"), http.StatusSeeOther)
}

// handleDenied is "This app needs your @iima.ac.in account", with a Switch account button.
func (s *Server) handleDenied(w http.ResponseWriter, r *http.Request) {
	rd := safeOr(r.URL.Query().Get("rd"), "/")
	sess := s.session(r)
	if sess == nil {
		http.Redirect(w, r, loginURL(rd), http.StatusFound)
		return
	}
	app := s.cfg.Registry.Lookup(rd)
	if app == nil || allowed(sess.User, app) {
		http.Redirect(w, r, rd, http.StatusFound)
		return
	}
	s.render(w, http.StatusForbidden, "denied", pageData{
		Title: "IIMA account needed", App: app, Session: sess, RD: rd,
		Retry: loginURL(rd) + "&switch=1", MemberDomain: s.cfg.MemberDomain,
	})
}

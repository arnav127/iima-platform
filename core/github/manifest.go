package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Creating the platform's App with GitHub's manifest flow: a super admin posts a manifest to GitHub,
// GitHub creates the App and sends them back with a one-time code, and core trades the code for the
// App's id, private key and webhook secret. Nobody copies keys around by hand.
// https://docs.github.com/en/apps/sharing-github-apps/registering-a-github-app-from-a-manifest

// Manifest describes the App: read-only access to a repo's contents (app.toml and its releases)
// and its release events, nothing else. It is public, so any student can install it on their repo.
func Manifest(name, publicURL string) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("github: the App needs a name")
	}
	base := strings.TrimRight(publicURL, "/")
	return json.Marshal(map[string]any{
		"name":            name,
		"url":             base,
		"description":     "Deploys student apps to " + base + " from their GitHub releases.",
		"public":          true,
		"redirect_url":    base + "/admin/github/created",
		"setup_url":       base + "/admin/apps/new",
		"hook_attributes": map[string]any{"url": base + "/platform/github/webhook", "active": true},
		"default_permissions": map[string]string{
			"contents": "read",
			"metadata": "read",
		},
		"default_events": []string{"release"},
	})
}

// NewAppURL is where the manifest is posted: a personal account, or an organization when org is set.
func NewAppURL(web, org, state string) string {
	if web == "" {
		web = "https://github.com"
	}
	web = strings.TrimRight(web, "/")
	q := "?state=" + url.QueryEscape(state)
	if org != "" {
		return web + "/organizations/" + url.PathEscape(org) + "/settings/apps/new" + q
	}
	return web + "/settings/apps/new" + q
}

// Created is what GitHub hands back for a new App.
type Created struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
	HTMLURL       string `json:"html_url"`
}

// Convert trades the one-time code from GitHub's redirect for the new App's credentials.
func Convert(ctx context.Context, api, code string) (*Created, error) {
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(api, "/")+"/app-manifests/"+url.PathEscape(code)+"/conversions", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github: creating the App: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var c Created
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&c); err != nil {
		return nil, err
	}
	if c.ID == 0 || c.PEM == "" || c.Slug == "" {
		return nil, errors.New("github: the answer had no App id, slug or key")
	}
	return &c, nil
}

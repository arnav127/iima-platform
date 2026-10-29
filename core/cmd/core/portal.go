package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BurntSushi/toml"

	"github.com/arnav127/iima-platform/core/github"
	"github.com/arnav127/iima-platform/core/jobgen"
	"github.com/arnav127/iima-platform/core/nomad"
	"github.com/arnav127/iima-platform/core/portal"
	"github.com/arnav127/iima-platform/core/registry"
	"github.com/arnav127/iima-platform/core/store"
)

// newPortal sets up the deploy portal from the environment. Without a GitHub App nobody can connect
// repos, and without Nomad nothing deploys, but the portal still serves the registry (files plus
// approved apps), so core always runs one.
//
//	GITHUB_APP_ID, GITHUB_APP_SLUG          the platform's GitHub App
//	GITHUB_APP_PRIVATE_KEY (PEM) or GITHUB_APP_PRIVATE_KEY_FILE
//	GITHUB_WEBHOOK_SECRET                   optional: without it the portal only polls
//	GITHUB_API_URL, GITHUB_WEB_URL          only to point at a fake GitHub
//	NOMAD_ADDR, NOMAD_TOKEN                 a token under the deploy policy
//	CLUSTER_CONFIG                          cluster.toml (datacenter, driver, public_url...)
//	ARTIFACT_URL                            where Nomad fetches builds from core (default: core's own address)
func newPortal(ctx context.Context, log *slog.Logger, st *store.Store, reg *registry.Registry, dataDir, addr string) (*portal.Portal, error) {
	cfg := portal.Config{
		Store: st, FileApps: reg.Apps(), Log: log,
		ArtifactDir: filepath.Join(dataDir, "artifacts"),
		ArtifactURL: env("ARTIFACT_URL", "http://"+addr+"/platform/artifacts"),
	}

	if id := os.Getenv("GITHUB_APP_ID"); id != "" {
		appID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("GITHUB_APP_ID: %w", err)
		}
		pemBytes := []byte(os.Getenv("GITHUB_APP_PRIVATE_KEY"))
		if f := os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"); len(pemBytes) == 0 && f != "" {
			if pemBytes, err = os.ReadFile(f); err != nil {
				return nil, err
			}
		}
		key, err := github.ParseKey(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY: %w", err)
		}
		slug := os.Getenv("GITHUB_APP_SLUG")
		if slug == "" {
			return nil, errors.New("GITHUB_APP_SLUG is needed with GITHUB_APP_ID (the App's name in its URL)")
		}
		cfg.GitHub = &github.App{ID: appID, Slug: slug, Key: key, WebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
			API: os.Getenv("GITHUB_API_URL"), Web: os.Getenv("GITHUB_WEB_URL")}
	}

	if a := os.Getenv("NOMAD_ADDR"); a != "" {
		cfg.Nomad = &nomad.Client{Addr: a, Token: os.Getenv("NOMAD_TOKEN")}
		if f := os.Getenv("CLUSTER_CONFIG"); f != "" {
			md, err := toml.DecodeFile(f, &cfg.Cluster)
			if err != nil {
				return nil, fmt.Errorf("CLUSTER_CONFIG: %w", err)
			}
			if und := md.Undecoded(); len(und) > 0 {
				return nil, fmt.Errorf("CLUSTER_CONFIG: unknown field %q", und[0].String())
			}
		} else {
			cfg.Cluster = jobgen.Cluster{Datacenter: "dc1", Driver: "exec"}
		}
		if cfg.Cluster.PublicURL == "" {
			cfg.Cluster.PublicURL = env("PUBLIC_URL", "http://localhost:8080")
		}
	}

	p, err := portal.New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	log.Info("portal", "github", cfg.GitHub != nil, "nomad", cfg.Nomad != nil, "artifacts", cfg.ArtifactURL)
	return p, nil
}

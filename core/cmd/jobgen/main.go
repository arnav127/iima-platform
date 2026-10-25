// Command jobgen prints the Nomad job for an app in the registry, until the deploy portal (phase 8)
// does this itself.
//
//	go run ./cmd/jobgen -app auxesiscapital -version v1.2.0 \
//	    -artifact https://github.com/arnav127/auxesis-capital/releases/download/v1.2.0/app.tar.gz \
//	    -sha256 <hash> > auxesiscapital.json
//	nomad job run -json auxesiscapital.json
//
// Cluster settings come from -cluster (default ../deploy/nomad/cluster.toml).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/arnav127/iima-platform/core/jobgen"
	"github.com/arnav127/iima-platform/core/registry"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jobgen:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		appID       = flag.String("app", "", "app id from the registry (required)")
		registryDir = flag.String("registry", "../apps", "registry directories, comma separated")
		clusterFile = flag.String("cluster", "../deploy/nomad/cluster.toml", "cluster settings")
		rel         jobgen.Release
	)
	rel.Env = map[string]string{}
	flag.StringVar(&rel.Version, "version", "", "release version (tag or commit)")
	flag.StringVar(&rel.ArtifactURL, "artifact", "", "URL of the release tar.gz")
	flag.StringVar(&rel.SHA256, "sha256", "", "SHA-256 of the artifact")
	flag.StringVar(&rel.Command, "command", "", "local mode: path of the binary to run (raw_exec only)")
	flag.StringVar(&rel.PublicDir, "public-dir", "", "local mode: built frontend directory")
	flag.Func("env", "extra non-secret env, KEY=VALUE (repeatable)", func(s string) error {
		k, v, ok := strings.Cut(s, "=")
		if !ok || k == "" {
			return errors.New("use KEY=VALUE")
		}
		rel.Env[k] = v
		return nil
	})
	flag.Parse()
	if *appID == "" {
		flag.Usage()
		return errors.New("-app is required")
	}

	reg, err := registry.LoadDirs(strings.Split(*registryDir, ",")...)
	if err != nil {
		return err
	}
	app := reg.ByID(*appID)
	if app == nil {
		return fmt.Errorf("no app %q in the registry", *appID)
	}
	var c jobgen.Cluster
	md, err := toml.DecodeFile(*clusterFile, &c)
	if err != nil {
		return err
	}
	if und := md.Undecoded(); len(und) > 0 {
		return fmt.Errorf("%s: unknown field %q", *clusterFile, und[0].String())
	}
	job, err := jobgen.Build(c, app, rel)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"Job": job})
}

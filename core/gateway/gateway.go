// Package gateway turns the registry's external routes (apps still running outside the platform)
// into Traefik dynamic configuration. Core serves it at /platform/routes and Traefik polls it with
// its HTTP provider, so a route added in /admin/routes is live within seconds, with no reload.
//
// Each route behaves like Apache's ProxyPass + ProxyPassReverse + ProxyPreserveHost:
//
//	ProxyPass        /path/x on the site goes to <upstream>/x (the upstream's own path kept in front)
//	ProxyPassReverse redirects and cookie paths that point at the upstream come back as /path
//	PreserveHost     the upstream sees Host: students.iima.ac.in (on by default)
//
// The identity rules are the platform's: browsers' X-User-* headers and the platform session cookie
// never reach the upstream, and with auth "optional" or "required" it gets the same verified
// X-User-Token as any app. Websockets pass through as they are; realtime routes skip compression, so
// server-sent events reach the browser as they are sent.
package gateway

import (
	"fmt"

	"github.com/arnav127/iima-platform/core/registry"
)

// Config is the subset of Traefik's dynamic configuration the routes use, as JSON.
type Config struct {
	HTTP HTTP `json:"http"`
}

type HTTP struct {
	Routers           map[string]Router           `json:"routers,omitempty"`
	Middlewares       map[string]map[string]any   `json:"middlewares,omitempty"`
	Services          map[string]Service          `json:"services,omitempty"`
	ServersTransports map[string]ServersTransport `json:"serversTransports,omitempty"`
}

type Router struct {
	Rule        string   `json:"rule"`
	Priority    int      `json:"priority"`
	EntryPoints []string `json:"entryPoints"`
	Service     string   `json:"service"`
	Middlewares []string `json:"middlewares"`
}

type Service struct {
	LoadBalancer LoadBalancer `json:"loadBalancer"`
}

type LoadBalancer struct {
	Servers          []Server `json:"servers"`
	PassHostHeader   bool     `json:"passHostHeader"`
	ServersTransport string   `json:"serversTransport,omitempty"`
}

type Server struct {
	URL string `json:"url"`
}

type ServersTransport struct {
	InsecureSkipVerify bool `json:"insecureSkipVerify"`
}

// EntryPoint is the public entry point's name in Traefik's static config.
const EntryPoint = "public"

// Routes builds the Traefik configuration for every external route in apps; other apps are
// ignored (their routes come from Nomad).
func Routes(apps []*registry.App) Config {
	h := HTTP{
		Routers:           map[string]Router{},
		Middlewares:       map[string]map[string]any{},
		Services:          map[string]Service{},
		ServersTransports: map[string]ServersTransport{},
	}
	for _, a := range apps {
		if a.Shape != registry.ShapeExternal {
			continue
		}
		name := "ext-" + a.ID
		server, upPath := a.UpstreamParts()

		// Applied after forward auth has seen the full path: map /path/x onto <upstream path>/x.
		var mapping []string
		if a.Strips() {
			h.Middlewares[name+"-strip"] = map[string]any{"stripPrefix": map[string]any{"prefixes": []string{a.Path}}}
			mapping = append(mapping, name+"-strip")
		}
		if upPath != "" {
			h.Middlewares[name+"-prefix"] = map[string]any{"addPrefix": map[string]any{"prefix": upPath}}
			mapping = append(mapping, name+"-prefix")
		}
		public := a.Path
		if a.IsRoot() {
			public = ""
		}
		h.Middlewares[name+"-reverse"] = map[string]any{"plugin": map[string]any{"proxyreverse": map[string]any{
			"upstream": server + upPath, "upstreamPath": upPath, "publicPath": public,
		}}}
		mapping = append(mapping, name+"-reverse")

		mw := func(auth string) []string {
			l := []string{"strip-identity@file"}
			if auth != registry.AuthNone {
				l = append(l, "verify-"+auth+"@file")
			}
			l = append(l, "strip-session@file", "security-headers-next@file")
			if !a.Realtime {
				l = append(l, "compress@file")
			}
			return append(l, mapping...)
		}

		h.Routers[name] = Router{Rule: a.Rule(""), Priority: a.Priority(false), EntryPoints: []string{EntryPoint}, Service: name, Middlewares: mw(a.Auth)}
		for i, p := range a.Public {
			h.Routers[fmt.Sprintf("%s-public-%d", name, i)] = Router{Rule: a.Rule(p), Priority: a.Priority(true), EntryPoints: []string{EntryPoint}, Service: name, Middlewares: mw(registry.AuthNone)}
		}

		lb := LoadBalancer{Servers: []Server{{URL: server}}, PassHostHeader: a.PreservesHost()}
		if a.InsecureTLS {
			lb.ServersTransport = "ext-insecure"
			h.ServersTransports["ext-insecure"] = ServersTransport{InsecureSkipVerify: true}
		}
		h.Services[name] = Service{LoadBalancer: lb}
	}
	return Config{HTTP: h}
}

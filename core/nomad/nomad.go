// Package nomad is the deploy portal's small client for Nomad's HTTP API: submit a job, keep an app's
// secrets in its Nomad Variable, create its data volume, and read its status and recent logs.
// Core calls it with a token under the "deploy" ACL policy (deploy/nomad/acl/deploy.policy.hcl).
package nomad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/arnav127/iima-platform/core/jobgen"
)

var ErrNotFound = errors.New("nomad: not found")

type Client struct {
	Addr  string // e.g. http://127.0.0.1:4646
	Token string
	HTTP  *http.Client
}

// SubmitJob registers (or updates) a job and returns the evaluation id.
func (c *Client) SubmitJob(ctx context.Context, j *jobgen.Job) (string, error) {
	var res struct{ EvalID string }
	err := c.call(ctx, http.MethodPut, "/v1/jobs", map[string]any{"Job": j}, &res)
	return res.EvalID, err
}

// StopJob stops a job's allocations (the job and its history stay, so a deploy brings it back).
func (c *Client) StopJob(ctx context.Context, job string) error {
	err := c.call(ctx, http.MethodDelete, "/v1/job/"+url.PathEscape(job), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// VariablePath is where an app's secrets live; its job template reads every key into the env.
func VariablePath(app string) string { return "nomad/jobs/" + app }

// Variable returns a variable's items, or an empty map when it does not exist.
func (c *Client) Variable(ctx context.Context, path string) (map[string]string, uint64, error) {
	var res struct {
		Items       map[string]string
		ModifyIndex uint64
	}
	err := c.call(ctx, http.MethodGet, "/v1/var/"+path, nil, &res)
	if errors.Is(err, ErrNotFound) {
		return map[string]string{}, 0, nil
	}
	if res.Items == nil {
		res.Items = map[string]string{}
	}
	return res.Items, res.ModifyIndex, err
}

// SetSecret adds or replaces (value != nil) or removes (value == nil) one key in path. It writes with
// check-and-set, so two owners saving at once cannot silently drop each other's change.
func (c *Client) SetSecret(ctx context.Context, path, key string, value *string) error {
	items, index, err := c.Variable(ctx, path)
	if err != nil {
		return err
	}
	if value == nil {
		delete(items, key)
	} else {
		items[key] = *value
	}
	q := fmt.Sprintf("?cas=%d", index)
	if len(items) == 0 {
		if index == 0 {
			return nil
		}
		return c.call(ctx, http.MethodDelete, "/v1/var/"+path+q, nil, nil)
	}
	return c.call(ctx, http.MethodPut, "/v1/var/"+path+q, map[string]any{"Path": path, "Items": items}, nil)
}

// EnsureHostVolume creates a dynamic host volume (plugin mkdir) unless one with that name exists.
// Nomad picks the node; the app's job is then pinned to it by the volume.
func (c *Client) EnsureHostVolume(ctx context.Context, name string) error {
	var vols []struct{ Name string }
	if err := c.call(ctx, http.MethodGet, "/v1/volumes?type=host", nil, &vols); err != nil {
		return err
	}
	for _, v := range vols {
		if v.Name == name {
			return nil
		}
	}
	vol := map[string]any{
		"Name": name, "Namespace": "default", "PluginID": "mkdir",
		"RequestedCapabilities": []map[string]string{{"AccessMode": "single-node-single-writer", "AttachmentMode": "file-system"}},
	}
	return c.call(ctx, http.MethodPut, "/v1/volume/host/create", map[string]any{"Volume": vol}, nil)
}

// Status is what the portal shows about a running app.
type Status struct {
	Job        string // running, pending, dead
	Version    uint64
	Deploy     string // the latest deployment: running, successful, failed...
	DeployNote string
	Release    string // Meta.version of the running job
}

func (c *Client) Status(ctx context.Context, job string) (*Status, error) {
	var j struct {
		Status  string
		Version uint64
		Meta    map[string]string
	}
	if err := c.call(ctx, http.MethodGet, "/v1/job/"+url.PathEscape(job), nil, &j); err != nil {
		return nil, err
	}
	s := &Status{Job: j.Status, Version: j.Version, Release: j.Meta["version"]}
	var d *struct{ Status, StatusDescription string }
	if err := c.call(ctx, http.MethodGet, "/v1/job/"+url.PathEscape(job)+"/deployment", nil, &d); err == nil && d != nil {
		s.Deploy, s.DeployNote = d.Status, d.StatusDescription
	}
	return s, nil
}

// Logs returns the end of the newest allocation's stdout and stderr for the "app" task.
func (c *Client) Logs(ctx context.Context, job string, max int) (stdout, stderr string, err error) {
	var allocs []struct {
		ID         string
		CreateTime int64
	}
	if err := c.call(ctx, http.MethodGet, "/v1/job/"+url.PathEscape(job)+"/allocations", nil, &allocs); err != nil {
		return "", "", err
	}
	if len(allocs) == 0 {
		return "", "", ErrNotFound
	}
	sort.Slice(allocs, func(i, k int) bool { return allocs[i].CreateTime > allocs[k].CreateTime })
	read := func(kind string) (string, error) {
		q := url.Values{"task": {"app"}, "type": {kind}, "origin": {"end"}, "offset": {fmt.Sprint(max)}, "plain": {"true"}}
		resp, err := c.raw(ctx, http.MethodGet, "/v1/client/fs/logs/"+allocs[0].ID+"?"+q.Encode(), nil)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)))
		return string(b), err
	}
	if stdout, err = read("stdout"); err != nil {
		return "", "", err
	}
	stderr, err = read("stderr")
	return stdout, stderr, err
}

// ---- plumbing ----

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.raw(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

func (c *Client) raw(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Addr, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("X-Nomad-Token", c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nomad: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("nomad: %s %s: %s: %s", method, strings.SplitN(path, "?", 2)[0], resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

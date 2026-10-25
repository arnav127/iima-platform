# iimakit/pb

Puts a Go + PocketBase (v0.40) app behind the platform. A separate Go module, so `iimakit` itself
never depends on PocketBase.

```go
app := pb.New(pb.Config{
	AppID: "auxesiscapital",
	Access: func(e *core.RequestEvent, c *iimakit.Claims) (*core.Record, error) {
		return s.Access(c.Email, c.Name) // nil, nil: signed in, but not a user of this app
	},
	OnFirstSeen: func(e *core.RequestEvent, u *core.Record, c *iimakit.Claims) { /* sign-in log */ },
})
app.Start()
```

What it does by default (each can be turned off in `Config`): verifies `X-User-Token` and sets
`e.Auth` from `Access`, turns off PocketBase's own sign-in methods, mirrors roles into a `roles`
field, serves `--publicDir` as an SPA, applies `APP_URL`, proxy and backup settings from env, and
answers `/_/` and the superuser API only over an SSH tunnel. See the package comment for details.

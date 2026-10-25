# iimakit (Python)

Checks the gateway's `X-User-Token` in Python apps, the same way `iimakit` does in Go.

```python
from fastapi import Depends, FastAPI, Request
from iimakit import Claims, Verifier

app = FastAPI()
iima = Verifier("portfolioleague")  # the app id from app.toml

@app.get("/me")
def me(user: Claims = Depends(iima.require_user)):  # 401 when signed out
    return {"email": user.email, "admin": user.has_role("admin")}

@app.get("/public")
def public(request: Request):
    user = iima.user(request)  # None when signed out
```

A Next.js server calling this backend forwards the request's `X-User-Token` header; the backend
checks it like any other request, so it never trusts a user id it is handed.

- Config from env: `IIMA_JWKS_URL` (set by the platform on the cluster), `IIMA_ISSUER`.
- Dev mode: `IIMA_DEV_USER=you@iima.ac.in` (and `IIMA_DEV_ROLES=admin`) when there is no token.
  Ignored on the cluster.
- Install by path until it is published: `pip install ../iima-platform/kit-py`.

Tests: `python -m unittest discover -s tests` (needs `pip install "pyjwt[crypto]"`).

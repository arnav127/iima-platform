"""An app on students.iima.ac.in: FastAPI with the platform's sign-in.

The gateway signs people in and puts a verified token on every request; iimakit checks it. The
gateway also strips the app's path, so routes here start at "/" (the public prefix arrives in
X-Forwarded-Prefix, for building links).
"""

from html import escape

from fastapi import Depends, FastAPI, Request
from fastapi.responses import HTMLResponse

from iimakit import Claims, Verifier

APP_ID = "mypyapp"  # the id in app.toml

app = FastAPI(title="My Python App", docs_url=None, redoc_url=None, openapi_url=None)
iima = Verifier(APP_ID)


@app.get("/health")
def health():
    """The platform's health check."""
    return {"ok": True}


@app.get("/api/me")
def me(user: Claims = Depends(iima.require_user)):
    """The signed-in person, as the platform sees them (401 when signed out)."""
    return {"email": user.email, "name": user.name, "programme": user.programme, "admin": user.has_role("admin")}


@app.get("/", response_class=HTMLResponse)
def home(request: Request):
    prefix = request.headers.get("x-forwarded-prefix", "")
    user = iima.user(request)
    if user is None:
        rd = escape(prefix + "/")
        body = f'<p><a href="/auth/login?rd={rd}">Sign in with your IIMA account</a></p>'
    else:
        body = f"<p>Signed in as <b>{escape(user.email)}</b> · <a href=\"/auth/logout\">Sign out</a></p>"
    return f"<!doctype html><meta charset=utf-8><title>My Python App</title><main style=\"font:15px system-ui;max-width:40rem;margin:3rem auto\"><h1>My Python App</h1>{body}</main>"

"""Platform kit for Python apps behind the students.iima.ac.in gateway.

The gateway puts a short-lived Ed25519 token in X-User-Token on every request from a signed-in user
the app's policy allows. This checks it against core's public keys, the same way iimakit.Verifier
does in Go:

    iima = Verifier("portfolioleague")

    @app.get("/api/me")
    def me(user: Claims = Depends(iima.require_user)):
        return {"email": user.email}

`iima.user(request)` returns None when signed out instead of failing. Works with any object that has
a `headers` mapping, so FastAPI is not a dependency.
"""

from __future__ import annotations

import json
import logging
import os
import threading
import time
import urllib.request
from dataclasses import dataclass, field
from typing import Any, Mapping

import jwt

__all__ = ["Claims", "TokenError", "Verifier", "TOKEN_HEADER", "dev_claims"]

TOKEN_HEADER = "x-user-token"  # browsers can never set it: the gateway strips it first
DEFAULT_ISSUER = "students.iima.ac.in"
CLAIMS_VERSION = 1

# At most one JWKS fetch per MIN_REFETCH seconds for unknown key ids, so a flood of forged tokens
# cannot hammer core; keys older than MAX_KEY_AGE are refreshed.
MIN_REFETCH = 10
MAX_KEY_AGE = 3600

log = logging.getLogger("iimakit")


class TokenError(Exception):
    """The token does not check out. Treat as signed out."""


@dataclass(frozen=True)
class Claims:
    """The identity token's payload (version 1). Mirrors iimakit.Claims."""

    sub: str
    email: str
    aud: str
    sid: str = ""
    name: str = ""
    picture: str = ""
    provider: str = ""
    member: bool = False
    affiliation: str = ""
    programme: str = ""
    batch: int = 0
    year: int = 0
    roles: tuple[str, ...] = field(default_factory=tuple)
    iat: int = 0
    exp: int = 0

    def has_role(self, role: str) -> bool:
        return role in self.roles

    @classmethod
    def from_payload(cls, p: Mapping[str, Any]) -> "Claims":
        return cls(
            sub=p["sub"], email=p["email"], aud=p["aud"], sid=p.get("sid", ""),
            name=p.get("name", ""), picture=p.get("picture", ""), provider=p.get("provider", ""),
            member=bool(p.get("member", False)), affiliation=p.get("affiliation", ""),
            programme=p.get("programme", ""), batch=int(p.get("batch", 0) or 0),
            year=int(p.get("year", 0) or 0), roles=tuple(p.get("roles") or ()),
            iat=int(p.get("iat", 0)), exp=int(p.get("exp", 0)),
        )


class Verifier:
    """Checks identity tokens for one app.

    Configured from the environment: IIMA_JWKS_URL (default: the local stack's JWKS) and
    IIMA_ISSUER (default students.iima.ac.in).
    """

    def __init__(self, app_id: str, jwks_url: str | None = None, issuer: str | None = None,
                 leeway: int = 30, timeout: float = 5.0):
        if not app_id:
            raise ValueError("iimakit: Verifier needs an app id")
        self.app_id = app_id
        self.jwks_url = jwks_url or os.getenv("IIMA_JWKS_URL") or "http://localhost:8080/auth/.well-known/jwks.json"
        self.issuer = issuer or os.getenv("IIMA_ISSUER") or DEFAULT_ISSUER
        self.leeway = leeway
        self.timeout = timeout
        self._lock = threading.Lock()
        self._keys: dict[str, Any] = {}
        self._fetched_at = float("-inf")

    def verify(self, token: str) -> Claims:
        try:
            header = jwt.get_unverified_header(token)
        except jwt.PyJWTError as e:
            raise TokenError("malformed token") from e
        # Only EdDSA: rules out "none" and HS256-with-the-public-key tricks.
        if header.get("alg") != "EdDSA" or not header.get("kid"):
            raise TokenError("token must be EdDSA with a key id")
        key = self._key(header["kid"])
        try:
            p = jwt.decode(
                token, key, algorithms=["EdDSA"], audience=self.app_id, issuer=self.issuer,
                leeway=self.leeway, options={"require": ["exp", "iat", "sub"], "strict_aud": True},
            )
        except jwt.PyJWTError as e:
            raise TokenError(str(e)) from e
        if p.get("v") != CLAIMS_VERSION:
            raise TokenError("unsupported token version")
        if not p.get("sub") or not p.get("email"):
            raise TokenError("token has no subject or email")
        # PyJWT checks this too; kept explicit so the rule does not depend on a library default.
        if p["iat"] > time.time() + self.leeway:
            raise TokenError("token issued in the future")
        return Claims.from_payload(p)

    def user(self, request: Any) -> Claims | None:
        """The signed-in user, or None. Rejected tokens are logged, never trusted. With
        IIMA_DEV_USER set and no token (off the cluster only), a fake user."""
        token = _header(request, TOKEN_HEADER)
        if not token:
            return dev_claims(self.app_id)
        try:
            return self.verify(token)
        except TokenError as e:
            log.warning("iimakit: rejected token: %s", e)
            return None

    def require_user(self, request: Any) -> Claims:
        """FastAPI dependency: the user, or a 401."""
        u = self.user(request)
        if u is None:
            from starlette.exceptions import HTTPException  # FastAPI apps have it; nothing else needs it
            raise HTTPException(status_code=401, detail="Sign in first")
        return u

    def _key(self, kid: str) -> Any:
        with self._lock:
            age = time.monotonic() - self._fetched_at
            if kid in self._keys and age < MAX_KEY_AGE:
                return self._keys[kid]
            if kid in self._keys:
                try:  # stale set: refresh, but keep the old keys if core is unreachable
                    self._fetch()
                except OSError:
                    pass
            elif age >= MIN_REFETCH:
                self._fetch()
            if kid in self._keys:
                return self._keys[kid]
        raise TokenError("token signed with an unknown key")

    def _fetch(self) -> None:
        self._fetched_at = time.monotonic()  # failed attempts count too
        with urllib.request.urlopen(self.jwks_url, timeout=self.timeout) as r:
            body = json.loads(r.read(1 << 20))
        keys = {}
        for k in body.get("keys", []):
            if k.get("kty") != "OKP" or k.get("crv") != "Ed25519" or not k.get("kid"):
                continue
            if k.get("use", "sig") != "sig" or k.get("alg", "EdDSA") != "EdDSA":
                continue
            keys[k["kid"]] = jwt.PyJWK(k, algorithm="EdDSA").key
        self._keys = keys


def dev_claims(app_id: str) -> Claims | None:
    """A fake user from IIMA_DEV_USER and IIMA_DEV_ROLES. Off on the cluster (NOMAD_ALLOC_ID set)."""
    email = os.getenv("IIMA_DEV_USER", "").strip().lower()
    if not email or os.getenv("NOMAD_ALLOC_ID"):
        return None
    roles = tuple(r.strip() for r in os.getenv("IIMA_DEV_ROLES", "").split(",") if r.strip())
    now = int(time.time())
    return Claims(sub="dev_" + email, email=email, aud=app_id, sid="dev", name=email.split("@")[0],
                  provider="dev", member=email.endswith("@iima.ac.in"), roles=roles, iat=now, exp=now + 3600)


def _header(request: Any, name: str) -> str | None:
    headers = getattr(request, "headers", request)
    v = headers.get(name)
    if v is None and hasattr(headers, "items"):
        # Plain dicts are case-sensitive; request headers in frameworks are not.
        v = next((val for k, val in headers.items() if k.lower() == name), None)
    return v or None

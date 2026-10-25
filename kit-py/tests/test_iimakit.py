import base64
import json
import os
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

import jwt
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat

import iimakit
from iimakit import Claims, TokenError, Verifier, dev_claims

KEY = Ed25519PrivateKey.generate()
OTHER = Ed25519PrivateKey.generate()
PUB_RAW = KEY.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw)
JWK = {"kty": "OKP", "crv": "Ed25519", "kid": "k1", "alg": "EdDSA", "use": "sig",
       "x": base64.urlsafe_b64encode(PUB_RAW).rstrip(b"=").decode()}
FETCHES = 0


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        global FETCHES
        FETCHES += 1
        body = json.dumps({"keys": [JWK]}).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


SERVER = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=SERVER.serve_forever, daemon=True).start()
JWKS = f"http://127.0.0.1:{SERVER.server_port}/jwks.json"


def claims(**over):
    now = int(time.time())
    c = {"v": 1, "iss": "students.iima.ac.in", "aud": "portfolioleague", "sub": "u_1", "sid": "s_1",
         "iat": now, "exp": now + 60, "email": "p25me@iima.ac.in", "provider": "google",
         "member": True, "roles": ["admin"]}
    c.update(over)
    return c


def sign(payload, kid="k1", key=KEY):
    headers = {"kid": kid} if kid else {}
    return jwt.encode(payload, key, algorithm="EdDSA", headers=headers)


class Req:
    def __init__(self, headers):
        self.headers = headers


class VerifierTest(unittest.TestCase):
    def setUp(self):
        for k in ("IIMA_DEV_USER", "IIMA_DEV_ROLES", "NOMAD_ALLOC_ID"):
            os.environ.pop(k, None)
        self.v = Verifier("portfolioleague", jwks_url=JWKS)

    def test_good_token(self):
        c = self.v.verify(sign(claims()))
        self.assertEqual(c.email, "p25me@iima.ac.in")
        self.assertTrue(c.has_role("admin"))
        self.assertFalse(c.has_role("writer"))

    def test_bad_tokens(self):
        now = int(time.time())
        payload = base64.urlsafe_b64encode(json.dumps(claims()).encode()).rstrip(b"=").decode()
        cases = {
            "wrong aud": sign(claims(aud="betamarkets")),
            "aud list": sign(claims(aud=["portfolioleague", "x"])),
            "expired": sign(claims(exp=now - 120, iat=now - 180)),
            "from the future": sign(claims(iat=now + 600, exp=now + 900)),
            "wrong issuer": sign(claims(iss="evil.example")),
            "wrong version": sign(claims(v=2)),
            "no email": sign(claims(email="")),
            "no kid": sign(claims(), kid=None),
            "unknown kid": sign(claims(), kid="k9"),
            "other key": sign(claims(), key=OTHER),
            "hs256 with public key": jwt.encode(claims(), PUB_RAW, algorithm="HS256", headers={"kid": "k1"}),
            "none": "eyJhbGciOiJub25lIiwia2lkIjoiazEifQ." + payload + ".",
            "garbage": "not.a.token",
        }
        for name, tok in cases.items():
            with self.subTest(name):
                with self.assertRaises(TokenError):
                    self.v.verify(tok)

    def test_unknown_kids_do_not_hammer_core(self):
        self.v.verify(sign(claims()))
        before = FETCHES
        for i in range(5):
            with self.assertRaises(TokenError):
                self.v.verify(sign(claims(), kid=f"x{i}"))
        self.assertLessEqual(FETCHES - before, 1)

    def test_user(self):
        self.assertIsNone(self.v.user(Req({})))
        self.assertEqual(self.v.user(Req({"X-User-Token": sign(claims())})).sub, "u_1")
        self.assertIsNone(self.v.user(Req({"x-user-token": sign(claims(aud="x"))})))

    def test_dev_user(self):
        os.environ["IIMA_DEV_USER"] = " Me@IIMA.ac.in "
        os.environ["IIMA_DEV_ROLES"] = "admin, writer"
        self.assertEqual(dev_claims("x").roles, ("admin", "writer"))
        self.assertEqual(self.v.user(Req({})).email, "me@iima.ac.in")
        # A rejected token is never replaced by the dev user.
        self.assertIsNone(self.v.user(Req({"x-user-token": sign(claims(aud="other"))})))
        os.environ["NOMAD_ALLOC_ID"] = "a1"
        self.assertIsNone(dev_claims("x"))


if __name__ == "__main__":
    unittest.main()

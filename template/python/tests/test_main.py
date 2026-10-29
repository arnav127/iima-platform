import os

from fastapi.testclient import TestClient

os.environ.pop("NOMAD_ALLOC_ID", None)

import main  # noqa: E402

client = TestClient(main.app)


def test_health():
    assert client.get("/health").json() == {"ok": True}


def test_signed_out():
    os.environ.pop("IIMA_DEV_USER", None)
    assert client.get("/api/me").status_code == 401
    assert "Sign in" in client.get("/").text


def test_forged_token_is_refused():
    os.environ.pop("IIMA_DEV_USER", None)
    r = client.get("/api/me", headers={"X-User-Token": "eyJhbGciOiJub25lIn0.e30."})
    assert r.status_code == 401


def test_dev_user():
    os.environ["IIMA_DEV_USER"] = "p25me@iima.ac.in"
    os.environ["IIMA_DEV_ROLES"] = "admin"
    try:
        assert client.get("/api/me").json()["admin"] is True
    finally:
        os.environ.pop("IIMA_DEV_USER")
        os.environ.pop("IIMA_DEV_ROLES")

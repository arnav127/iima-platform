# App template: Python (FastAPI)

The third starting point for an app on students.iima.ac.in. Sign-in comes from the platform:
`iimakit` (in `kit-py`) checks the gateway's token, so `Depends(iima.require_user)` is all a route
needs.

## Start a new app

1. Copy this folder to a new repo. Set `id` and `path` in `app.toml` and `APP_ID` in `main.py`; put
   your @iima.ac.in email in `owners`.
2. Push to GitHub and connect the repo at `/admin/apps/new`. After a super admin approves it, every push
   to `main` runs the tests, packs the code with its dependencies (`.github/workflows/deploy.yml`) and
   deploys.
3. Want a database? Set `pocketbase = true` in `app.toml`, add `pb_migrations/`, and set
   `PB_ADMIN_EMAIL` and `PB_ADMIN_PASSWORD` as secrets: the platform's PocketBase runs beside the app on
   loopback (`POCKETBASE_URL`), and only this app talks to it.

## Work on it

```
python -m venv .venv
.venv/Scripts/python -m pip install -r requirements-dev.txt ../iima-platform/kit-py
IIMA_DEV_USER=you@iima.ac.in .venv/Scripts/python -m uvicorn main:app --reload
.venv/Scripts/python -m pytest
```

The gateway strips the app's path, so routes start at `/`; links back to the app use the
`X-Forwarded-Prefix` header. Dependencies go in `requirements.txt`, pinned, and are installed for the
cluster's Python (3.12) at build time.

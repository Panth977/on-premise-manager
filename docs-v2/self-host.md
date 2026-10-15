# Self-hosting the OPM cloud on a VM (SPEC §21 #108)

The OPM cloud runs in two ways from ONE codebase:

| | Firebase (default, `your-firebase-project`) | VM (`opm-cloud`) |
|---|---|---|
| API `/v1/*` | Cloud Function `api` | `opm-cloud` (same router, same handlers) |
| Documents | Firestore | SQLite: `db/global.sqlite` + one `db/deployments/<id>.sqlite` per deployment |
| Objects | Cloud Storage bucket | files under `blobs/` (same object paths) |
| Sign-in | Firebase Auth | built-in: email + password (argon2id), optional OIDC / Google |
| Triggers (image import, GitHub mirror) | Firestore triggers | outbox rows written in the same SQLite transaction, drained in-process |
| Scheduled jobs (daily cleanup, mirror retry) | Cloud Scheduler | in-process scheduler (last run stored, missed runs catch up at start) |
| Download URLs | 5-minute GCS v4 signed URLs | 5-minute HMAC URLs `/_blob/…` served by `opm-cloud` |
| `staging/` lifecycle (1 day), TTL fields | bucket lifecycle, Firestore TTL | hourly / 10-minute sweep jobs |
| Frontend | Firebase Hosting | `opm-cloud` serves the same build (SPA fallback) |

The browser never talks to a database: everything goes through `/v1`. The frontend reads `/opm-config.json` at start
and uses Firebase Auth or the built-in sign-in accordingly, so the SAME frontend build runs on both.

---------------------------------------------------------------------------------------------------------------------

## 1. Install with Docker (recommended)

Requirements: a Linux VM with Docker Engine + the compose plugin, 2 vCPU, 2 GB RAM, disk for the data dir (images and
transfers are stored there — plan 20 GB+). Agents must reach the VM over HTTPS (or HTTP on a trusted network).

```sh
git clone <this repository> && cd on-premise-manager/selfhost
# optional: agent binaries for upload-agent (cd ../agent && make dist)
OPM_PUBLIC_URL=https://opm.example.com docker compose up -d --build
docker compose exec opm-cloud opm-cloud create-admin --email you@example.com      # prints a generated password once
docker compose exec opm-cloud opm-cloud release-key                                # agent signing key (data dir)
V=$(node -p "require('../agent/dist/release.json').version")   # upload-agent requires the version of the build
docker compose exec opm-cloud opm-cloud upload-agent --dir /agent-dist --version "$V" --latest
docker compose exec opm-cloud opm-cloud upload-deps --docker-version 28.5.2 --agent-version "$V"
docker compose restart opm-cloud     # picks up the release public key for install scripts
```

Sessions are revoked with one-second precision (like Firebase's `tokensValidAfterTime`).

Open `OPM_PUBLIC_URL`, sign in, and continue exactly as on the hosted cloud (registries, services, templates,
deployments, install commands). Every install / upgrade command, agent binary and the deps manifest come from THIS
server — agents talk to one domain only (the one-cloud-domain rule holds per installation).

The named volume `opm-data` (mounted at `/data`) **is** the whole cloud; `OPM_DATA=/srv/opm-data docker compose up -d`
binds a host directory instead (it must be writable by uid 10001: `sudo install -d -o 10001 /srv/opm-data`). Stop:
`docker compose down` (data stays; `down -v` deletes it). Logs: `docker compose logs -f opm-cloud` (one JSON line per entry).

### Plain Node + systemd (no Docker)

```sh
cd backend && npm ci && npm run build:server          # → backend/dist/opm-cloud.cjs (+ GitHub-mirror artefacts)
cd ../frontend && npm ci && npm run build             # → frontend/build
sudo install -d /opt/opm-cloud && sudo cp -r ../backend/dist /opt/opm-cloud/dist && sudo cp -r build /opt/opm-cloud/www
sudo useradd --system --home /var/lib/opm-cloud opm
sudo install -D -m 0640 /dev/null /etc/opm-cloud/env   # OPM_PUBLIC_URL=… OPM_SMTP_URL=… (see §2)
sudo cp ../selfhost/opm-cloud.service /etc/systemd/system/ && sudo systemctl enable --now opm-cloud
sudo -u opm env OPM_DATA_DIR=/var/lib/opm-cloud node /opt/opm-cloud/dist/opm-cloud.cjs create-admin --email you@example.com
```

Node 22.13+ (built-in `node:sqlite`); no native modules, no Firebase SDK.

## 2. Configuration

Environment variables (or a JSON file via `--config` / `OPM_CONFIG` with the camelCase keys):

| variable | default | meaning |
|---|---|---|
| `OPM_ADAPTER` | `local` | `local` (SQLite + files + built-in auth) or `firebase` (Firestore/Storage/Auth from a VM; needs `npm i firebase-admin` and Google credentials; triggers + schedules then come from the deployed Cloud Functions unless `OPM_RUN_JOBS=1`) |
| `OPM_DATA_DIR` | `/var/lib/opm-cloud` (`/data` in Docker) | everything: `db/`, `blobs/`, `blobmeta/`, `secrets/`, `tmp/` |
| `OPM_PUBLIC_URL` | `http://localhost:8790` | the URL agents and browsers use (install commands, blob URLs, reset links) |
| `OPM_CONSOLE_URL` | = public URL | console origin if it differs |
| `OPM_PORT` / `OPM_HOST` | `8790` / `0.0.0.0` | listener |
| `OPM_STATIC_DIR` | (Docker: `/opt/opm-cloud/www`) | the built frontend; unset = API only |
| `OPM_FRONTEND_ORIGINS` | public + console origin | extra CORS origins for human routes |
| `OPM_BLOB_REDIRECT` | `1` | 302 large downloads to 5-minute `/_blob` HMAC URLs (`0` = always stream) |
| `OPM_TRUST_PROXY` | `1` | take the client IP from `X-Forwarded-For` |
| `OPM_SMTP_URL`, `OPM_SMTP_FROM` | – | e-mail for "Forgot password" and admin reset links (without SMTP the console hides "Forgot password"; admins copy reset links from the Accounts page) |
| `OPM_OIDC_ISSUER`, `OPM_OIDC_CLIENT_ID`, `OPM_OIDC_CLIENT_SECRET`, `OPM_OIDC_LABEL`, `OPM_OIDC_ALLOWED_DOMAIN` | – | optional single sign-on (Google: issuer `https://accounts.google.com`, redirect URI `<public URL>/auth/oidc/callback`); a person still needs an OPM account with the same e-mail |
| `OPM_RELEASE_PUBLIC_KEY` | data dir key | the agent release key the install script verifies binaries with |
| `OPM_AUTH_SECRET`, `OPM_BLOB_SECRET` | generated in `secrets/` | HS256 ID-token key, blob-URL HMAC key |

Sign-in sessions: 1-hour ID tokens (HS256) + 30-day rotating refresh tokens; changing the password, "Set new password",
disabling an account and revoking all end every session (same rules as Firebase Auth's `tokensValidAfterTime`).

## 3. HTTPS

Agents and browsers should reach `opm-cloud` over HTTPS. Either:

- **Built-in Caddy profile**: `OPM_DOMAIN=opm.example.com OPM_PUBLIC_URL=https://opm.example.com docker compose --profile https up -d`
  (ports 80/443 on the VM, automatic Let's Encrypt; `selfhost/Caddyfile`), or
- **your reverse proxy** (nginx, Traefik, a load balancer): proxy everything to `127.0.0.1:8790`, keep `X-Forwarded-For`,
  allow request bodies ≥ 64 MiB, read/write timeouts ≥ 2 min (long polls are ≤ 50 s; transfers use 8 MiB parts), disable
  response buffering for `/v1/` and `/_blob/`.

For a private CA, give the agents the CA via `/etc/opm/extra-ca.pem` (SPEC §1.4).

## 4. Backups

```sh
docker compose exec opm-cloud opm-cloud backup --out /data/backups/$(date +%F)    # online, consistent
docker compose cp opm-cloud:/data/backups/$(date +%F) ./opm-backup-$(date +%F)       # copy it off the volume
```

`backup` makes a `VACUUM INTO` copy of every SQLite file (safe while the server runs), copies `blobs/`, `blobmeta/`
and `secrets/`, and writes `BACKUP.json`. Copy the backup directory off the VM. Restore: stop the server, replace the
data dir with the backup (`db/`, `blobs/`, `blobmeta/`, `secrets/`), start. A file-level snapshot of the whole data dir
taken while the server is stopped is also a valid backup. `secrets/` holds the session, blob-URL and release keys: keep
backups private.

## 5. Upgrade

```sh
git pull && cd selfhost && docker compose up -d --build     # or rebuild dist/ + www/ and restart the unit
```

The SQLite schema upgrades itself (new index columns are added and back-filled at start). Upgrade agents as usual from
the console (Setup → Upgrade) after `opm-cloud upload-agent … --latest` + `upload-deps`.

## 6. Migrate from Firebase

From a repository checkout with Firebase credentials (`gcloud auth login`; `OPM_GCLOUD_TOKEN=1` uses the gcloud user):

```sh
cd backend
OPM_GCLOUD_TOKEN=1 npx tsx scripts/migrate-cloud.ts --from firebase --to local --project your-firebase-project \
  --data-dir /path/to/new/data --dry-run                     # counts only
OPM_GCLOUD_TOKEN=1 npx tsx scripts/migrate-cloud.ts --from firebase --to local --project your-firebase-project \
  --data-dir /path/to/new/data                               # copy + verify
```

- Reads Firebase only. Copies every document (recursive walk, the same JSON; Timestamps / bytes encoded losslessly), every
  Storage object (sha256-verified), every Auth user with the SAME uid (so `accounts/{uid}` keeps working).
- Verification: per-collection counts and a sha256 over the canonical encoding of each document must match, every
  object hash must match; the report is written to `<data dir>/migration-report.json`; exit code 1 on any difference.
- Copied documents do not re-fire triggers (no re-import, no mirror replay).
- **Passwords cannot be exported from Firebase**: migrated users have no password. Run
  `opm-cloud create-admin --email <you>` for your own admin login, then send everyone a reset link (Accounts → Reset
  password, or `opm-cloud reset-link --email <e>`; mailed when SMTP is configured).
- Then point agents at the new cloud: they keep the API base from their install command, so re-run each site's
  **upgrade command** from the new console (it rewrites `apiBase` in `/etc/opm/agent.json`), or edit `apiBase` and restart
  the agent.
- The reverse (`--from local --to firebase … --confirm-project <id>`) exists for completeness; it writes into the
  Firebase project and requires the confirmation flag.

## 7. Limits of the VM edition

- One `opm-cloud` process per data dir (jobs are leased, but the outbox dispatcher and caches assume one server). Scale
  up, not out; the documented next adapter (pg + redis + s3) is the path to several instances (the ports are ready for it).
- SQLite files must be on a local disk (not NFS / SMB).

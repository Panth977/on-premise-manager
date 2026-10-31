# On-Prem Manager (OPM)

Run and update Docker applications on servers you don't have easy access to: customer VMs, factory floors, air-gapped
networks. OPM works like Portainer, plus a git-style commit history for every server's config.

- **Cloud console**: the catalog of images, deployment templates, variables, secrets and files. You commit a change once
  and review the diff per site.
- **Agent** (one Go binary per server): applies commits to the server's Docker, with health checks, a countdown and
  ACCEPT, and automatic rollback. It snapshots volumes before each update and can also snapshot them on a schedule.
- **Standalone first**: every server's agent GUI can do everything locally, with no cloud needed. You can create
  containers, change images, edit files, variables and secrets. Docker stays the source of truth: changes made directly
  in Docker show up as uncommitted drift, or are auto-committed.
- **Offline friendly**: sites without internet reach the cloud through a peer site, or through the browser of the
  person using the agent GUI (the internet tunnel).
- **Docker-only last resort**: the cloud can mirror every branch to a GitHub repository. Each mirrored branch runs its
  sites with no OPM code at all: plain `docker compose`, Caddy as the router and Portainer as the GUI. For when a client
  allows neither the agent nor the cloud. Each machine keeps its own git branch, merges new cloud commits and runs
  `./deploy.sh`. See [docs-v2/github-mirror.md](docs-v2/github-mirror.md).

## Layout

| Path | What |
|---|---|
| `agent/` | the agent (Go): Docker driver, edge proxy (Caddy), branch/commit engine, snapshots, dev tools |
| `agent/gui/` | the agent GUI (SvelteKit, embedded into the binary) |
| `frontend/` | the cloud console (SvelteKit) |
| `agent/cmd/opm-cloud`, `agent/internal/cloud/` | the cloud API (Go, one process on a VM: SQLite + local blobs) |
| `shared/` | contracts shared by every part: types, validation, expressions, hashing, test vectors |
| `ui/` | shared Svelte form components and design tokens of both GUIs |
| `selfhost/` | Dockerfile, docker compose, Caddyfile and systemd unit of the cloud |
| `e2e/` | end-to-end harness (real agents and Docker) |
| `docs-v2/` | documentation. `SPEC-final.md` is the full specification |

## Quick start: self-host the cloud

```sh
git clone https://github.com/Panth977/on-premise-manager && cd on-premise-manager/selfhost
export OPM_CLOUD_IMAGE=ghcr.io/panth977/on-premise-manager:latest
OPM_PUBLIC_URL=https://opm.example.com docker compose up -d --no-build
docker compose exec opm-cloud opm-cloud create-admin --email you@example.com   # prints a password once
```

For HTTPS with automatic certificates, set `OPM_DOMAIN` and add `--profile https`. To build the image yourself, drop
`OPM_CLOUD_IMAGE` and `--no-build`.

Agent binaries are attached to each [GitHub release](https://github.com/Panth977/on-premise-manager/releases). Build
them yourself with `cd agent && make gui dist`, then upload them to your cloud with `opm-cloud upload-agent`. After that,
the cloud's Setup page gives you a one-line install command per server. All steps are in
[docs-v2/self-host.md](docs-v2/self-host.md).

## Development

Requirements: Node 22, Go ≥ 1.26 (`GOTOOLCHAIN=auto` downloads it), and Docker for the integration tests.

```sh
(cd shared && npm ci && npm run build && npm test)
(cd ui && npm ci)
(cd agent/gui && npm ci && npm run build)          # builds into agent/internal/webui/dist
(cd agent && go test -short ./...)                  # drop -short to run the Docker tests too
(cd frontend && npm ci && npm run check)
(cd agent && go test ./internal/cloud/apitest)      # API suite against the real opm-cloud binary
```

`cd agent && go run ./cmd/opm-devmesh` starts an in-memory fake cloud with two agents, which is useful for GUI work.

## License

[Apache-2.0](LICENSE)

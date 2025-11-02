# gprxy: sso-first, psql-compatible proxy: role mapping, audit trails, and pooled connections.

[![CI](https://github.com/sathwick-p/gprxy/actions/workflows/release.yml/badge.svg)](https://github.com/sathwick-p/gprxy/actions/workflows/release.yml)
[![License: MPL-2.0](https://img.shields.io/badge/License-MPL--2.0-brightgreen.svg)](./LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24+-blue.svg)](go.mod)
[![Container](https://img.shields.io/badge/ghcr.io-sathwick--p%2Fgprxy-informational)](https://github.com/sathwick-p/gprxy/pkgs/container/gprxy)

gprxy is a Go-based PostgreSQL proxy that authenticates users with OAuth/OIDC (e.g., Auth0), maps their roles to PostgreSQL service accounts, and pools backend DB connections. It speaks the native PostgreSQL wire protocol, supports SCRAM/MD5, relays cancel requests correctly, and optionally serves TLS to clients. Run it locally or in Kubernetes behind an NLB.

### Why gprxy
- **SSO-first access (no DB passwords to manage)**: Developers authenticate via your IdP (OIDC). You don’t distribute or rotate per-user DB passwords.
- **Per-user audit trail**: Logs include the actual user identity so you know who ran which queries, aiding investigations and compliance.
- **Centralized RBAC**: Map IdP roles to database service accounts (free-form role names). Enforce least privilege and combine with Postgres RLS.
- **Operationally safe scaling**: Connection pooling keeps DB connection counts low while serving many users.
- **Drop-in psql compatibility**: Native PostgreSQL protocol, cancel requests, SCRAM/MD5, and optional client→proxy TLS.

## Table of Contents
- [Quickstart](#quickstart)
- [Features](#features)
- [Install](#install)
- [Configuration](#configuration)
- [Usage](#usage)
- [CLI Reference](#cli-reference)
- [Design & Architecture](#design--architecture)
- [Development & Testing](#development--testing)
- [Troubleshooting / FAQ](#troubleshooting--faq)
- [Deployment (Kubernetes)](#deployment-kubernetes)
- [Security](#security)
- [Changelog / Releases](#changelog--releases)
- [License](#license)

## Quickstart
- Requires Go 1.24+, PostgreSQL reachable at `DB_HOST:5432`.

```bash
# 1) Clone and enter
git clone https://github.com/sathwick-p/gprxy.git
cd gprxy

# 2) Configure environment (create a .env)
cat > .env <<'EOF'
GPRXY_USER=app_writer
GPRXY_PASS=app_writer_password
DB_HOST=localhost
LOG_LEVEL=debug

# OAuth/OIDC (proxy + CLI)
AUTH0_TENANT=your-tenant.us.auth0.com
AUTH0_NATIVE_CLIENT_ID=your-native-client-id
CALLBACK_URL=http://localhost:8085/callback
AUDIENCE=https://gprxy.io

# CLI target
PROXY_URL=localhost

# Role mapping (free-form roles; examples)
ROLE_MAPPING_ADMIN=postgres:postgres_password
DEFAULT_ROLE=admin
EOF

# 3) Build
go build -o gprxy .

# 4) Start the proxy
./gprxy start

# 5) Login (opens browser for PKCE)
./gprxy login
```

Connect with psql using your access token:
```bash
TOKEN=$(jq -r .access_token ~/.gprxy/credentials)
PGPASSWORD="$TOKEN" psql -h localhost -p 7777 -U your.email@company.com -d postgres -c "select now();"
```

## Features
- **SSO-based access (no DB creds for devs)** with OAuth/OIDC and JWKS caching (Auth0 today).
- **Per-user audit logs** — see who ran which queries through the proxy.
- **RBAC via role mapping**: free‑form IdP roles → PostgreSQL service accounts; pair with RLS for isolation.
- **Connection pooling** (per service-user and database) using pgxpool.
- **Full protocol handling**: SCRAM‑SHA‑256/MD5, SSLRequest/CancelRequest, ReadyForQuery, etc.
- **Optional TLS** for client→proxy; clean fallback when not configured.
- **CLI** for SSO login and simplified connectivity.

## Install
- From source:
  - Go 1.24+
  - `go build -o gprxy .`
- Docker:
  - `docker build -t gprxy:local .`
  - `docker run -p 7777:7777 --env-file .env gprxy:local`
- GitHub Actions:
  - Releases are built on tags (`v*.*.*`) and containers are published to GHCR.

## Configuration
gprxy reads environment variables (and `.env` if present). Required values must be set or the process will exit with a clear message.

| Category | Variable | Default | Required | Description |
|---|---|---:|:---:|---|
| Proxy | `PROXY_HOST` | `0.0.0.0` |  | Listen address |
| Proxy | `PROXY_PORT` | `7777` |  | Listen port (PostgreSQL protocol) |
| Backend | `DB_HOST` | `localhost` |  | PostgreSQL server hostname |
| Backend | `GPRXY_USER` | — | yes | Service account username (for pooled connections) |
| Backend | `GPRXY_PASS` | — | yes | Service account password |
| TLS | `PROXY_CERT` | — |  | Path to PEM cert; enables TLS if set |
| TLS | `PROXY_KEY` | — |  | Path to PEM key |
| Logging | `LOG_LEVEL` | `production` |  | `debug` for verbose logs |
| OAuth (proxy) | `AUTH0_TENANT` | — | yes | Auth0 domain (e.g., `example.us.auth0.com`) |
| OAuth (proxy) | `AUDIENCE` | — | yes | Token audience (e.g., `https://gprxy.io`) |
| Role mapping | `ROLE_MAPPING_<ROLE>` | — |  | Map OAuth role to `username:password` (any role name) |
| Role mapping | `DEFAULT_ROLE` | — |  | Fallback role if user has no mapped roles |
| CLI (login) | `AUTH0_NATIVE_CLIENT_ID` | — | yes | Native app client ID |
| CLI (login) | `CALLBACK_URL` | — | yes | e.g., `http://localhost:8085/callback` |
| CLI | `CONNECTION_NAME` | — |  | Optional Auth0 connection to preselect |
| CLI | `PROXY_URL` | — | yes | Hostname to reach the proxy (NLB in k8s or `localhost` locally) |

Notes:
- Roles are free-form; use any role names and provide matching `ROLE_MAPPING_<ROLE>=username:password`.
- `PROXY_URL` should be your proxy’s hostname: NLB when deployed to Kubernetes, or `localhost` during local dev.

## Usage
Minimal flow:

1) Start the proxy
```bash
./gprxy start
```

2) Authenticate (opens a browser)
```bash
./gprxy login
# Tokens saved at ~/.gprxy/credentials
```

3) Connect through the proxy using psql and your JWT
```bash
TOKEN=$(jq -r .access_token ~/.gprxy/credentials)
PGPASSWORD="$TOKEN" psql -h <proxy-host> -p 7777 -U your.email@company.com -d <db>
```

4) Or use the built-in connect helper (experimental)
```bash
# Requires: PROXY_URL, and flags for DB host and database
./gprxy connect -s <db_host> -d <database> [-p 5432]
```

### TLS
TLS code exists and works locally with the self‑signed certs in `certs/`. I haven’t yet figured out a simple, user‑friendly way to let everyone run it directly, open to suggestions and contributions.

## CLI Reference
- `gprxy`: root command; version is injected at build time via `-X main.Version`.
- `gprxy start`: start the proxy server.
- `gprxy login`: PKCE login; starts a local server on `:8085/callback`, exchanges tokens, stores `~/.gprxy/credentials`. Auto‑refresh supported.
- `gprxy connect -s <host> -d <db> [-p 5432]`: connect through the proxy using saved credentials; upgrades to TLS if proxy supports it.

Flags (connect):
- `-s, --host`: DB hostname or IP (required)
- `-d, --database`: DB name (required)
- `-p, --port`: DB port (default 5432)

## Design & Architecture
High-level flow:
```
Client (psql) --[TLS optional]--> gprxy --[TCP]--> PostgreSQL (5432)
           \__________________ PostgreSQL wire protocol ______________/
```

- Authentication
  - Proxy asks client for password (cleartext over TLS if enabled).
  - If the “password” looks like a JWT, gprxy validates it with cached JWKS and maps roles to a PostgreSQL service account using `ROLE_MAPPING_*` (free‑form roles).
  - Otherwise, proxy performs standard auth against PostgreSQL (SCRAM/MD5) on a temporary connection.

- Connection pooling
  - Queries run on pooled connections keyed by (service‑user, database).
  - Defaults and timeouts are set in `internal/pool/manager.go`.

- Cancel requests
  - Client receives `BackendKeyData` for the pooled connection.
  - A later `CancelRequest` is looked up in a registry and forwarded to the backend using the 16‑byte cancel message.

See docs for deeper details:
- `docs/architecture.md`
- `docs/auth-scram.md`
- `docs/authentication-oauth-oidc.md`
- `docs/tls.md`
- `docs/connection-behavior.md`
- `docs/cancel-requests.md`

## Development & Testing
- Build
```bash
go build -o gprxy .
```

- Run
```bash
./gprxy start
```

- Tests
```bash
go test -v ./...
```

- Make targets
```bash
make help
make build
make run
make test
make clean
make docker-build
make docker-push
```

- CI: GitHub Actions (`.github/workflows/release.yml`) builds multi‑arch binaries and publishes GHCR images on tags.

## Troubleshooting / FAQ
- TLS handshake issues
  - Ensure `PROXY_CERT`/`PROXY_KEY` are set and readable if you expect TLS.
  - If using self‑signed certs, configure trust on your client system.

- Two quick “connections” in logs when using psql
  - Normal behavior. psql probes for the auth method then reconnects with credentials. Provide credentials via env or `.pgpass` to avoid it. See `docs/connection-behavior.md`.

- JWT rejected
  - Verify `AUTH0_TENANT` and `AUDIENCE`. Check token expiration. Ensure your token contains the roles you expect (names are free‑form) and that you’ve provided matching `ROLE_MAPPING_*` envs or `DEFAULT_ROLE`.

- Queries succeed but permissions look too broad
  - The pooled service account executes queries. Grant least‑privilege to that service account. Consider RLS and session personalization on the DB.

- Cancel doesn’t work
  - Ensure the client received `BackendKeyData` (sent from the pooled connection during startup). See `docs/cancel-requests.md`.

- CLI login hangs
  - Check that your browser opened the Auth0 page and that `CALLBACK_URL` matches `http://localhost:8085/callback`. Ensure port 8085 is open locally.

- Proxy cannot connect to DB
  - Verify `DB_HOST:5432` is reachable from where the proxy runs. Check firewalls/VPC/security groups.

## Deployment (Kubernetes)
Manifests in `k8s/`:
- Deployment (`k8s/deployment.yaml`): 3 replicas, port `7777`, env from secret `gprxy-config`.
- Service (`k8s/service.yaml`): exposes port `7777` in‑cluster.
- Secret (`k8s/secret.yaml`): example keys — `DB_HOST`, `GPRXY_USER`, `GPRXY_PASS`, `AUTH0_TENANT`, `AUDIENCE`, `ROLE_MAPPING_*`, `DEFAULT_ROLE`, `LOG_LEVEL`. Add TLS files/paths via secret or mounts if needed.

For clients, set `PROXY_URL` to the Service/NLB hostname (e.g., `gprxy-nlb.<...>.elb.amazonaws.com`).

## Security
- Use TLS for client→proxy in production where possible. Avoid logging secrets/tokens. Scope PostgreSQL service accounts to least‑privilege. Combine with Row‑Level Security (RLS) as appropriate.
- No formal disclosure program; please open a GitHub issue or PR with details.

## Changelog / Releases
- See `docs/changelog.md`.
- Release artifacts are published on tags `v*.*.*` (multi‑arch binaries + GHCR image).



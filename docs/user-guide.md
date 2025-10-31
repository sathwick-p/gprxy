# gprxy User Guide

This guide explains what gprxy is, what it implements today, how it works, and how to use it in development and production. It consolidates the codebase and docs into a single, practical reference.

## What is gprxy?

gprxy is a PostgreSQL proxy focused on:
- Terminating client connections (optionally with TLS)
- Handling authentication on behalf of the backend
- Pooling backend connections for efficiency
- Supporting OAuth/OIDC tokens mapped to database service accounts
- Preserving cancel requests and protocol semantics

At a high level:
```
Client ↔ gprxy ↔ PostgreSQL
```

Clients connect to gprxy using the normal PostgreSQL wire protocol. gprxy authenticates the client, then forwards queries over a pooled service-user connection to the database.


## Key features

- TLS for client→proxy connections (server mode)
- Authentication handled by the proxy
  - Password-based auth (SCRAM-SHA-256, MD5)
  - OAuth/OIDC JWTs via Auth0, mapped to service accounts
- Connection pooling (per user + database) using pgxpool
- Cancel requests (Ctrl+C or CancelRequest) routed correctly
- Protocol transparency: ReadyForQuery, ParameterStatus, key messages
- Simple, structured logging with debug/production modes
- CLI for SSO login and database connectivity


## Architecture overview

Startup and connection handling:
1. Client connects to gprxy (optionally upgrades to TLS).
2. gprxy receives the StartupMessage and initiates authentication.
3. Proxy authenticates the client using a temporary backend connection.
4. On success, gprxy acquires a pooled connection (service user) for queries.
5. gprxy sends BackendKeyData (from the pooled connection) and ReadyForQuery to the client.
6. Queries are relayed between client and backend until the connection closes.

Cancel requests:
- gprxy registers active connections by PID and SecretKey.
- When the client sends a CancelRequest, gprxy looks up the target session and forwards a raw 16‑byte cancel message to PostgreSQL.

See also:
- Architecture and code flow: architecture.md
- TLS specifics: tls.md
- Authentication details (SCRAM): auth-scram.md
- OAuth/OIDC integration: authentication-oauth-oidc.md
- Cancel requests: cancel-requests.md
- Connection behavior notes: connection-behavior.md


## System requirements

- Go 1.21+
- PostgreSQL 13+ (works with later versions as well)
- Auth0 tenant (for OAuth/OIDC flows), when using token-based auth


## Building and running

### Build
```bash
# From the repo root
go build -o gprxy .
```

### Start the proxy (server)
```bash
# Configure environment (see "Configuration")
./gprxy start
```

The proxy listens on DB_HOST:7777. TLS is optional but recommended for client connections.


## Configuration

gprxy loads configuration from system env and optional .env.

Core proxy settings:
- GPRXY_USER: Service user for backend pool (required)
- GPRXY_PASS: Service user password for backend pool (required)
- DB_HOST: PostgreSQL host (default: localhost)
- LOG_LEVEL: debug or production (default: production)

TLS (client→proxy) settings:
- PROXY_CERT: Path to TLS certificate (enables TLS if set)
- PROXY_KEY: Path to TLS private key

OAuth/OIDC (Auth0) settings for token-based auth:
- AUTH0_TENANT: Auth0 tenant domain (e.g., example.us.auth0.com)
- AUDIENCE: API audience used in tokens
- AUTH0_NATIVE_CLIENT_ID: Native application client ID
- CALLBACK_URL: http://localhost:8085/callback (or your chosen URL)
- CONNECTION_NAME: Optional; Auth0 connection to pre-select

CLI runtime settings:
- PROXY_URL: Hostname of the running proxy (e.g., localhost)

Example .env (development):
```bash
# Proxy → DB (pool user)
GPRXY_USER=app_writer
GPRXY_PASS=app_writer_password
DB_HOST=localhost

# Proxy logging
LOG_LEVEL=debug

# TLS for client→proxy
PROXY_CERT=certs/postgres.crt
PROXY_KEY=certs/postgres.key

# OAuth/OIDC for CLI and proxy
AUTH0_TENANT=your-tenant.us.auth0.com
AUTH0_NATIVE_CLIENT_ID=your-native-client-id
CALLBACK_URL=http://localhost:8085/callback
AUDIENCE=https://gprxy.io
CONNECTION_NAME=your-connection

# CLI target
PROXY_URL=localhost
```


## Authentication modes

### 1) Password-based (SCRAM-SHA-256, MD5)
- The proxy initiates a temporary connection to PostgreSQL
- Asks the client for a password using AuthenticationCleartextPassword
- Completes SCRAM/MD5 with the backend on behalf of the client
- On success, switches the client to a pooled service-user connection

See: auth-scram.md

### 2) OAuth/OIDC (JWT via Auth0)
- Client uses gprxy CLI to get and cache tokens
- CLI connects to the proxy and sends the JWT as the "password"
- Proxy validates JWT locally (JWKS cache), extracts roles
- Roles are mapped to a PostgreSQL service account
- Queries run under that service account via pooling

See: authentication-oauth-oidc.md


## Connection pooling

- Pools are keyed by (service user, database)
- pgxpool is used with reasonable defaults (max 5, idle handling, timeouts)
- Each client gets a dedicated connection from the pool for the duration of the session

See: internal/pool


## Cancel requests

- Client receives BackendKeyData for the pooled connection (not the temp auth connection)
- CancelRequest is sent to gprxy over a new connection
- gprxy forwards the raw cancel message (PID, SecretKey) to PostgreSQL

See: cancel-requests.md


## TLS

- Client→proxy TLS follows the standard PostgreSQL SSLRequest/response flow
- Enable by setting PROXY_CERT and PROXY_KEY
- Proxy→PostgreSQL is currently plain TCP (can be enabled in future)

See: tls.md


## Logging

- LOG_LEVEL=production (default): concise operational logs
- LOG_LEVEL=debug: protocol-level messages, pool stats, timings

Examples:
```text
[INFO] PostgreSQL proxy listening on 127.0.0.1:7777 (TLS: enabled)
[INFO] connection request - user: alice@company.com, database: mydb, app: psql
[DEBUG] backend connection established in 28.6ms
```

See: logging.md


## Using the CLI

The CLI helps you log in (SSO) and connect to databases using your cached token.

### Login (SSO)
```bash
# Opens a browser for SSO; caches credentials in ~/.gprxy/credentials
./gprxy login
```

What it does:
- PKCE Authorization Code flow with Auth0
- Starts a local callback server on http://localhost:8085/callback
- Exchanges the authorization code for tokens
- Saves access/refresh tokens and basic user info to ~/.gprxy/credentials
- Handles auto-refresh when the token nears expiration

### Connect to a database
```bash
# Required: --host (-s) and --database (-d)
./gprxy connect -s proxy.host.example -d mydb -p 7777
```
Notes:
- The CLI connects to $PROXY_URL:7777 (set PROXY_URL in your env) and speaks PostgreSQL wire protocol
- It sends the JWT token as a PasswordMessage
- On success, it opens an interactive session relaying stdin/stdout to the proxy connection

Flags (connect):
- --host, -s: Database or proxy hostname (required)
- --database, -d: Database name (required)
- --port, -p: Port (default 5432 for validation; proxy is 7777 at runtime)

## Client behavior and tips

- psql may create two connections when prompting for passwords interactively
  - First: probe connection (disconnects quickly)
  - Second: real connection with credentials
- To avoid the “double connection” pattern, provide credentials via .pgpass or environment variables

See: connection-behavior.md


## Quick start (local)

1) Generate local TLS certs (optional, recommended for testing TLS client→proxy)
```bash
# See tls.md for commands and server.cnf template
```

2) Configure .env
```bash
GPRXY_USER=app_writer
GPRXY_PASS=app_writer_password
DB_HOST=localhost
LOG_LEVEL=debug
PROXY_CERT=certs/postgres.crt
PROXY_KEY=certs/postgres.key
AUTH0_TENANT=your-tenant.us.auth0.com
AUTH0_NATIVE_CLIENT_ID=your-native-client-id
CALLBACK_URL=http://localhost:8085/callback
AUDIENCE=https://gprxy.io
PROXY_URL=localhost
```

3) Build and run the proxy
```bash
go build -o gprxy .
./gprxy start
```

4) Authenticate
```bash
./gprxy login
```

5) Connect to a database through the proxy
```bash
./gprxy connect -s localhost -d postgres
```


## Troubleshooting

- TLS handshake failed (client→proxy)
  - Ensure PROXY_CERT/PROXY_KEY are set and readable
  - Match hostname with certificate (for verify-full)

- Authentication succeeds but queries fail
  - Check the service user’s permissions in PostgreSQL
  - Confirm connection pool user/database mapping is correct

- Cancel requests do nothing
  - Ensure the client received BackendKeyData for the pooled connection
  - Verify active connections are present in the registry (debug logs)

- Token refresh errors
  - Confirm AUTH0_TENANT and AUTH0_NATIVE_CLIENT_ID
  - Check that ~/.gprxy/credentials is readable and has a refresh token

- Double connection seen in logs
  - Expected for interactive psql; avoid by using .pgpass or environment variables


## Security notes

- Always use TLS for client→proxy in production
- Do not log full tokens or passwords
- Keep service account permissions as minimal as possible
- Combine proxy-side identity with database-side RLS for strong isolation


## Roadmap ideas

- TLS for proxy→PostgreSQL
- Role personalization (SET ROLE) per session with more granular controls
- Metrics and health endpoints

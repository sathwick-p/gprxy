# OAuth 2.0 and OIDC Integration

This document consolidates the implementation guide with conceptual background. The content is preserved from the original files.

---

## Implementation guide (original `oauth.md`)

# OAuth 2.0 Integration with PostgreSQL: Implementation Guide for gprxy

## Table of Contents
1. [Overview](#overview)
2. [PostgreSQL OAuth Capabilities](#postgresql-oauth-capabilities)
3. [Architecture Options](#architecture-options)
4. [Detailed Setup Instructions](#detailed-setup-instructions)
5. [Implementation Guide](#implementation-guide)
6. [Security Considerations](#security-considerations)
7. [Troubleshooting](#troubleshooting)

---

## Overview

This document provides a comprehensive guide for implementing OAuth 2.0 authentication in `gprxy` (a PostgreSQL connection proxy). It covers:

- **Option 1**: PostgreSQL 18+ native OAuth with pass-through authentication
- **Option 2**: Proxy-side token validation with role mapping (recommended for most setups)

The choice depends on your PostgreSQL version, infrastructure, and security requirements.

### Current Architecture (Password-Based)

```
Client (psql)
    │ user=sathwick.p, password=****
    ↓
gprxy Proxy
    │ Opens temp connection to PostgreSQL
    │ Performs SCRAM-SHA-256/MD5 auth
    ↓
PostgreSQL
    │ Validates password against stored hash
    ↓
Connection Pool
```

### Recommended Future Architecture (Option 2: OAuth)

```
Client (psql)
    │ user=sathwick.p, oauth_token=JWT
    ↓
gprxy Proxy
    │ 1. Validates JWT locally (JWKS cache)
    │ 2. Extracts roles/groups from token claims
    │ 3. Maps role → service account (admin/writer/reader)
    │ 4. Sets session variables with user context
    ↓
PostgreSQL
    │ Connections from pool (postgres/app_writer/app_reader)
    │ Row-Level Security (RLS) enforces access control
    ↓
Data (filtered by RLS)
```

---

## PostgreSQL OAuth Capabilities

### PostgreSQL 17 and Earlier
- **Authentication Methods**: SCRAM-SHA-256, MD5, cleartext passwords only
- **Token Support**: None (must validate tokens externally)
- **Implication**: You manage all OAuth validation in the proxy

### PostgreSQL 18+
- **Native OAuth Support**: Built-in OAuth 2.0 authentication method
- **JWT Validation**: JWKS (JSON Web Key Set) support with local caching
- **Key Features**:
  - Token validation using cached public keys (no per-connection Auth0 call)
  - Role mapping from token claims
  - Configurable issuer, audience, JWKS endpoint
  - Custom validators via extensions
  - Token introspection caching

#### JWT Validation Flow in PostgreSQL 18

```
PostgreSQL 18 Startup:
    │
    ├─ 1. Download public keys (once)
    │  GET https://auth0.com/.well-known/jwks.json
    │  Cache for hours/days
    │
    └─ Ready to accept connections

Per-Connection:
    │
    ├─ 2. Client connects with JWT token
    │  oauth_token=eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...
    │
    ├─ 3. PostgreSQL validates locally:
    │  Signature verification (cached keys)
    │  Issuer (iss claim) matches config
    │  Audience (aud claim) matches config
    │  Expiration (exp claim) not exceeded
    │  Extract roles from custom claims
    │
    └─ 4. Map to PostgreSQL role and authenticate
        (No network call to Auth0!)
```

---

## Architecture Options

### Option 1: PostgreSQL 18+ Native OAuth (Pass-Through)

**When to use:**
- PostgreSQL 18+ already deployed
- Want PostgreSQL as sole authentication authority
- Minimal custom validation logic needed
- Already have Auth0/OIDC provider configured

**When NOT to use:**
- PostgreSQL < 18
- Complex custom authorization rules needed
- Need to modify/filter tokens before PostgreSQL sees them
- Need extensive logging/monitoring at proxy level

```
┌────────────┐              ┌─────────────┐                ┌──────────────┐
│   Client   │              │    Proxy    │                │PostgreSQL 18 │
│  (psql)    │              │   (gprxy)   │                │   (OAuth)    │
└─────┬──────┘              └──────┬──────┘                └──────┬───────┘
      │                            │                               │
      │ 1. StartupMessage          │                               │
      │    user=sathwick.p         │                               │
      │    oauth_token=JWT         │                               │
      │───────────────────────────>│                               │
      │                            │                               │
      │                            │ 2. StartupMessage             │
      │                            │    (passes through)           │
      │                            │───────────────────────────────>│
      │                            │                               │
      │                            │    3. PostgreSQL validates    │
      │                            │       JWT with JWKS cache     │
      │                            │       Maps token claims       │
      │                            │       to roles                │
      │                            │                               │
      │                            │ 4. AuthenticationOK           │
      │                            │<───────────────────────────────│
      │                            │                               │
      │ 5. AuthenticationOK        │                               │
      │<───────────────────────────│                               │
      │                            │                               │
      │ 6. Queries execute with    │                               │
      │    token-mapped roles      │                               │
```

**Advantages:**
- PostgreSQL handles all validation
- Minimal proxy changes
- Excellent performance (JWKS cached)
- Centralized token authority

**Disadvantages:**
- Requires PostgreSQL 18+
- Limited proxy-side control
- Less visibility into validation failures
- Harder to implement custom rules

---

### Option 2: Proxy-Side Token Validation (Recommended)

**When to use:**
- Any PostgreSQL version (13+)
- Need custom authorization logic
- Want centralized validation in proxy
- Need detailed logging/monitoring
- Want to map multiple OAuth providers to same DB

**When NOT to use:**
- Token validation becomes bottleneck (unlikely with caching)
- Proxy unavailable → no new connections (less resilient)

```
┌────────┐           ┌──────────────┐              ┌──────────────┐
│ Client │           │    Proxy     │              │ PostgreSQL   │
│ (psql) │           │   (gprxy)    │              │  (any ver)   │
└───┬────┘           └──────┬───────┘              └──────┬───────┘
    │                       │                            │
    │ 1. Connect            │                            │
    │    oauth_token=JWT    │                            │
    │──────────────────────>│                            │
    │                       │                            │
    │                       │ 2. Validate JWT locally:   │
    │                       │    • Fetch JWKS if needed  │
    │                       │    • Verify signature      │
    │                       │    • Check iss, aud, exp   │
    │                       │    • Extract claims        │
    │                       │    • JWKS cache (24h+)     │
    │                       │                            │
    │                       │ 3. Map role to PG user:    │
    │                       │    admin  → postgres       │
    │                       │    writer → app_writer     │
    │                       │    reader → app_reader     │
    │                       │                            │
    │                       │ 4. Open connection as:     │
    │                       │    user=app_writer         │
    │                       │    password=<secure>       │
    │                       │───────────────────────────>│
    │                       │                            │
    │                       │ 5. SCRAM auth             │
    │                       │<───────────────────────────│
    │                       │                            │
    │ 6. AuthenticationOK   │                            │
    │<──────────────────────│                            │
    │                       │                            │
    │ 7. Execute as user    │                            │
    │    context: user_id   │                            │
    │    context: role      │                            │
```

**Advantages:**
- Works with any PostgreSQL version (13+)
- Full control over validation logic
- Can implement custom authorization rules
- Better observability and debugging
- Easier token manipulation/filtering
- Works with multiple OAuth providers
- Maintained security boundary at proxy

**Disadvantages:**
- Proxy-side complexity increases
- Need to manage service account credentials
- Token validation is proxy responsibility
- Proxy becomes critical path (but fast due to JWKS caching)

---

## Detailed Setup Instructions

### Part 1: Auth0 Configuration

#### 1.1 Create Auth0 Application

1. Log in to [Auth0 Dashboard](https://manage.auth0.com)
2. Navigate to **Applications → Applications → Create Application**
3. Name: `gprxy` | Type: **Machine to Machine Applications**
4. Authorized Grant Types: **Authorization Code**, **Refresh Token Rotation**
5. Save the credentials (Client ID, Client Secret, Domain)

#### 1.2 Create Auth0 API

1. Navigate to **APIs → Create API**
2. Name: `PostgreSQL Database`
3. Identifier: `https://gprxy.io` (used as audience)
4. Signing Algorithm: `RS256`
5. Enable **RBAC** (Role-Based Access Control)

#### 1.3 Configure Auth0 Actions (Role Mapping)

Create custom claims in tokens:

1. **Flows → Actions → Create Action**
2. Name: `Add Custom Claims`
3. Trigger: **Login / Post-Login**
4. Code:
```javascript
exports.onExecutePostLogin = async (event, api) => {
  // Get user roles from Auth0
  const roles = event.authorization?.roles || [];
  
  // Map to PostgreSQL roles
  const pgRoles = roles.map(role => {
    if (role === 'admin') return 'pg_admin';
    if (role === 'writer') return 'pg_writer';
    return 'pg_reader';
  });

  // Add custom namespace claims (Auth0 requires namespace)
  api.idToken.setCustomClaim('https://gprxy.io/roles', pgRoles);
  api.idToken.setCustomClaim('https://gprxy.io/user_id', event.user.user_id);
  api.idToken.setCustomClaim('https://gprxy.io/email', event.user.email);
  api.idToken.setCustomClaim('https://gprxy.io/org_id', event.organization?.id || null);
};
```
5. Deploy and add to **Login** flow

#### 1.4 Configure Auth0 Tenant Settings

```
Settings → General Tab:
- Domain: test-org-flow.us.auth0.com
- Client ID: <your-client-id>
- Client Secret: <your-client-secret>

Settings → Advanced:
- JWT Signing Algorithm: RS256
- Backup Public Key: Enabled
```

Save configuration to `/auth0/config.json`:
```json
{
  "AUTH0_DOMAIN": "test-org-flow.us.auth0.com",
  "AUTH0_CLIENT_ID": "your-client-id-here",
  "AUTH0_CLIENT_SECRET": "your-client-secret-here",
  "POSTGRES_API_AUDIENCE": "https://gprxy.io",
  "JWKS_ENDPOINT": "https://test-org-flow.us.auth0.com/.well-known/jwks.json",
  "JWKS_CACHE_TTL_SECONDS": 86400
}
```

---

### Part 2: PostgreSQL Setup

#### PostgreSQL 18+ (Option 1: Native OAuth)

##### 2.1 PostgreSQL Configuration (postgresql.conf)

```bash
# Enable OAuth validator
oauth_validator_libraries = 'oauth_jwt_validator'

# Auth0 JWKS Configuration
oauth.jwt_validator.issuer = 'https://test-org-flow.us.auth0.com/'
oauth.jwt_validator.audience = 'https://gprxy.io'
oauth.jwt_validator.jwks_uri = 'https://test-org-flow.us.auth0.com/.well-known/jwks.json'
oauth.jwt_validator.jwks_cache_ttl = 86400  # 24 hours

# Role claim mapping (match Auth0 custom claim)
oauth.jwt_validator.role_claim = 'https://gprxy.io/roles'

# Additional settings
oauth.jwt_validator.allowed_algorithms = 'RS256'

# Optional: Require specific scopes
oauth.jwt_validator.required_scope = 'openid profile email'

# Connection timeout for JWKS fetch
oauth.jwt_validator.connect_timeout = 10000  # milliseconds
```

**Location**: `/var/lib/postgresql/data/postgresql.conf` (or PostgreSQL data directory)

**Apply changes:**
```bash
# Option A: Reload without restart (if parameters allow it)
sudo -u postgres psql -c "SELECT pg_reload_conf();"

# Option B: Restart PostgreSQL
sudo systemctl restart postgresql
```

**Verify configuration:**
```sql
-- Connect as superuser
SHOW oauth_validator_libraries;
SHOW oauth.jwt_validator.issuer;
SHOW oauth.jwt_validator.jwks_uri;
```

##### 2.2 PostgreSQL Role Setup (Any Version, Native OAuth)

Create base roles for permission levels:

```sql
-- Connect as PostgreSQL superuser
-- psql -U postgres -d postgres

-- Create role hierarchy
CREATE ROLE pg_admin WITH NOLOGIN INHERIT;
CREATE ROLE pg_writer WITH NOLOGIN INHERIT;
CREATE ROLE pg_reader WITH NOLOGIN INHERIT;

-- Grant database privileges
GRANT ALL PRIVILEGES ON DATABASE mydb TO pg_admin;
GRANT CONNECT ON DATABASE mydb TO pg_writer;
GRANT CONNECT ON DATABASE mydb TO pg_reader;

-- Connect to the target database
\c mydb

-- Create schema if it doesn't exist
CREATE SCHEMA IF NOT EXISTS public;

-- Grant schema privileges
GRANT USAGE ON SCHEMA public TO pg_admin;
GRANT USAGE ON SCHEMA public TO pg_writer;
GRANT USAGE ON SCHEMA public TO pg_reader;

-- Grant table privileges (admin)
GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE, 
      REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA public TO pg_admin;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE, 
        REFERENCES, TRIGGER ON TABLES TO pg_admin;

-- Grant table privileges (writer)
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO pg_writer;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO pg_writer;

-- Grant table privileges (reader)
GRANT SELECT ON ALL TABLES IN SCHEMA public TO pg_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT SELECT ON TABLES TO pg_reader;

-- Sequence privileges
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO pg_admin;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO pg_writer;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT USAGE, SELECT ON SEQUENCES TO pg_admin;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT USAGE, SELECT ON SEQUENCES TO pg_writer;

-- Function privileges
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO pg_admin;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO pg_writer;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO pg_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT EXECUTE ON FUNCTIONS TO pg_admin;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT EXECUTE ON FUNCTIONS TO pg_writer;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT EXECUTE ON FUNCTIONS TO pg_reader;

-- Create per-user login roles (these inherit from role hierarchy)
CREATE ROLE sathwick_p LOGIN;
GRANT pg_admin TO sathwick_p;  -- This user is admin

CREATE ROLE alice LOGIN;
GRANT pg_writer TO alice;  -- This user is writer

CREATE ROLE bob LOGIN;
GRANT pg_reader TO bob;  -- This user is reader

-- Verify setup
\du+  -- List all roles with details
```

##### 2.3 PostgreSQL Host-Based Authentication (pg_hba.conf) - OAuth

**Location**: `/var/lib/postgresql/data/pg_hba.conf`

```bash
# Type    Database    User    Address         Method    Options
# ====    ========    ====    =======         ======    =======

# Allow OAuth authentication over TCP (before any password lines)
host      all         all     0.0.0.0/0       oauth     issuer=https://test-org-flow.us.auth0.com/,scope="openid profile email"

# Or with explicit role mapping (if pg_ident.conf is configured)
host      all         all     0.0.0.0/0       oauth     issuer=https://test-org-flow.us.auth0.com/,scope="openid profile email",map=oauth_map

# Fallback: Local connections with peer authentication (Unix domain socket)
local     all         all                     peer

# Reject everything else
host      all         all     0.0.0.0/0       reject
```

**Apply changes:**
```bash
sudo -u postgres psql -c "SELECT pg_reload_conf();"
```

##### 2.4 PostgreSQL User Mapping (pg_ident.conf) - Optional

**Location**: `/var/lib/postgresql/data/pg_ident.conf`

Maps OAuth identities to PostgreSQL roles if they don't match:

```bash
# MAPNAME        OAUTH-IDENTITY                           POSTGRES-USERNAME
oauth_map        waad|aidash-sso|b8972ee5-f637-4037-...   sathwick_p
oauth_map        sathwick.p@aidash.com                     sathwick_p
oauth_map        auth0|65a1b2c3d4e5f6g7h8i9j0k1l2m         alice
oauth_map        google-oauth2|118287234756234764987      bob
```

**Important Notes:**
- Left side: OAuth identity (sub claim or email)
- Right side: PostgreSQL username (must exist)
- Multiple entries per MAPNAME allowed
- MAPNAME must match pg_hba.conf

**Apply changes:**
```bash
sudo -u postgres psql -c "SELECT pg_reload_conf();"
```

##### 2.5 PostgreSQL Row-Level Security (RLS)

Enable RLS for multi-tenant isolation:

```sql
-- Connect to target database
\c mydb

-- Enable RLS on all tables
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
ALTER TABLE comments ENABLE ROW LEVEL SECURITY;

-- Create security policies
CREATE POLICY users_isolation ON users 
  USING (owner_id = current_setting('app.user_id')::uuid)
  WITH CHECK (owner_id = current_setting('app.user_id')::uuid);

CREATE POLICY posts_isolation ON posts 
  USING (owner_id = current_setting('app.user_id')::uuid 
         OR visibility = 'public')
  WITH CHECK (owner_id = current_setting('app.user_id')::uuid);

-- Bypass RLS for admin role
ALTER ROLE pg_admin NOINHERIT;  -- Must check role membership explicitly
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
CREATE POLICY admin_bypass ON users 
  AS PERMISSIVE
  FOR ALL
  TO pg_admin
  USING (true);
```

---

#### PostgreSQL (Any Version) - Option 2: Proxy-Side Validation

##### 2.1 PostgreSQL Role Setup (Service Accounts)

```sql
-- Connect as PostgreSQL superuser
-- psql -U postgres -d postgres

-- Create service accounts for proxy to use
CREATE ROLE pg_admin LOGIN ENCRYPTED PASSWORD 'secure-admin-password-here';
CREATE ROLE pg_writer LOGIN ENCRYPTED PASSWORD 'secure-writer-password-here';
CREATE ROLE pg_reader LOGIN ENCRYPTED PASSWORD 'secure-reader-password-here';

-- Create role hierarchy (for inheritance)
CREATE ROLE admin_group NOINHERIT;
CREATE ROLE writer_group NOINHERIT;
CREATE ROLE reader_group NOINHERIT;

-- Grant roles to service accounts
GRANT admin_group TO pg_admin;
GRANT writer_group TO pg_writer;
GRANT reader_group TO pg_reader;

-- Switch to target database
\c mydb

-- Create schema
CREATE SCHEMA IF NOT EXISTS public;

-- Grant schema privileges
GRANT USAGE ON SCHEMA public TO admin_group;
GRANT USAGE ON SCHEMA public TO writer_group;
GRANT USAGE ON SCHEMA public TO reader_group;

-- Grant table privileges (admin)
GRANT ALL ON ALL TABLES IN SCHEMA public TO admin_group;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT ALL ON TABLES TO admin_group;

-- Grant table privileges (writer)
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO writer_group;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO writer_group;

-- Grant table privileges (reader)
GRANT SELECT ON ALL TABLES IN SCHEMA public TO reader_group;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT SELECT ON TABLES TO reader_group;

-- Sequence privileges
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO admin_group;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO writer_group;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT USAGE, SELECT ON SEQUENCES TO admin_group;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT USAGE, SELECT ON SEQUENCES TO writer_group;

-- Function privileges
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO admin_group;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO writer_group;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO reader_group;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT EXECUTE ON FUNCTIONS TO admin_group;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT EXECUTE ON FUNCTIONS TO writer_group;
ALTER DEFAULT PRIVILEGES IN SCHEMA public 
  GRANT EXECUTE ON FUNCTIONS TO reader_group;

-- Verify setup
\du+  -- List roles
```

##### 2.2 PostgreSQL Host-Based Authentication (pg_hba.conf)

```bash
# Type    Database    User            Address         Method
# ====    ========    ====            =======         ======

# Allow proxy service accounts with SCRAM (encrypted passwords)
host      mydb        pg_admin        127.0.0.1/32    scram-sha-256
host      mydb        pg_writer       127.0.0.1/32    scram-sha-256
host      mydb        pg_reader       127.0.0.1/32    scram-sha-256

# Allow from proxy server (update IP to your proxy)
host      mydb        pg_admin        192.168.1.0/24  scram-sha-256
host      mydb        pg_writer       192.168.1.0/24  scram-sha-256
host      mydb        pg_reader       192.168.1.0/24  scram-sha-256

# Local connections
local     all         all                             peer

# Reject everything else
host      all         all             0.0.0.0/0       reject
```

**Apply changes:**
```bash
sudo -u postgres psql -c "SELECT pg_reload_conf();"
```

##### 2.3 PostgreSQL Row-Level Security (RLS) - Multi-Tenant

```sql
\c mydb

-- Create tables with tenant context
CREATE TABLE users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL,
  user_id UUID NOT NULL,  -- Auth0 sub claim
  email TEXT NOT NULL,
  created_at TIMESTAMP DEFAULT now()
);

CREATE TABLE posts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL,
  owner_id UUID NOT NULL,  -- Auth0 user_id from token
  title TEXT NOT NULL,
  content TEXT,
  created_at TIMESTAMP DEFAULT now()
);

-- Enable RLS
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE posts ENABLE ROW LEVEL SECURITY;

-- Session variables will be set by proxy:
-- SET app.user_id = '...';
-- SET app.tenant_id = '...';
-- SET app.roles = '["admin"]';

-- Create RLS policies
CREATE POLICY tenant_isolation ON users 
  AS PERMISSIVE
  FOR SELECT
  USING (tenant_id = (current_setting('app.tenant_id', true))::uuid);

CREATE POLICY users_own_data ON users
  AS PERMISSIVE
  FOR UPDATE
  USING (user_id = (current_setting('app.user_id', true))::uuid)
  WITH CHECK (user_id = (current_setting('app.user_id', true))::uuid);

CREATE POLICY posts_visibility ON posts
  AS PERMISSIVE
  FOR SELECT
  USING (
    tenant_id = (current_setting('app.tenant_id', true))::uuid
    AND (
      owner_id = (current_setting('app.user_id', true))::uuid
      OR 'admin' = ANY(string_to_array(current_setting('app.roles', true), ','))
    )
  );

-- Verify policies
\d posts_visibility
```

---

## Implementation Guide

### Proxy Implementation for Option 2 (Recommended)



## Security Considerations

### 1. Token Transmission

**Problem**: Sending tokens in plaintext during connection

**Solutions:**
- Use TLS/SSL for client → proxy connection (mandatory)
- Use TLS for proxy → PostgreSQL connection
- Never log full tokens (truncate for debugging)

```go
// Example: Secure token logging
func logToken(token string) {
  parts := strings.Split(token, ".")
  if len(parts) >= 2 {
    logger.Debug("token: %s...%s", parts[0][:8], parts[len(parts)-1][len(parts[len(parts)-1])-8:])
  }
}
```

### 2. JWKS Caching

**Problem**: Fetching JWKS on every connection is slow

**Solution**: Implement multi-level caching with TTL

```
Level 1: In-memory cache (24 hours)
  ↓ (on miss or expiration)
Level 2: HTTP request to Auth0 JWKS endpoint
  ↓ (with exponential backoff on failure)
Level 3: Fallback to last-known-good keys
```

### 3. Token Expiration and Refresh

**Problem**: Tokens expire; client needs ability to refresh

**Options:**
- Option A: Client refreshes token before connecting
- Option B: Proxy implements refresh token rotation
- Option C: Session-based tokens with server-side refresh

**Recommendation**: Option A - Client manages refresh

```bash
# Client-side (in .gprxy/config.json)
{
  "access_token": "eyJhbGc...",
  "refresh_token": "eyJhbGc...",
  "expires_at": "2025-10-21T15:00:00Z",
  "token_endpoint": "https://test-org-flow.us.auth0.com/oauth/token"
}

# When connecting:
if current_time > expires_at - 60_seconds:
  fetch new token using refresh_token
```

### 4. Service Account Credentials (Option 2)

**Problem**: Proxy needs PostgreSQL service account passwords

**Solutions:**
- Store in environment variables (best for Docker/K8s)
- Use HashiCorp Vault for secret management
- Use AWS Secrets Manager / Azure Key Vault
- Use .env file (development only)

```bash
# .env (development, never commit)
PG_ADMIN_PASSWORD=secure-password-here
PG_WRITER_PASSWORD=secure-password-here
PG_READER_PASSWORD=secure-password-here

# Kubernetes Secret
kubectl create secret generic gprxy-db-creds \
  --from-literal=pg_admin_password=... \
  --from-literal=pg_writer_password=... \
  --from-literal=pg_reader_password=...
```

### 5. RLS Enforcement

**Problem**: Need to ensure user data isolation

**Solution**: Combine RLS with application-layer checks

```sql
-- Enforce at database level
CREATE POLICY strict_isolation ON users
  AS RESTRICTIVE  -- Always checked, cannot be bypassed by permissive policies
  USING (
    -- Current user's tenant must match
    current_setting('app.tenant_id')::uuid = tenant_id
    -- Additional checks (e.g., subscription active)
    AND EXISTS (
      SELECT 1 FROM tenant_subscriptions ts
      WHERE ts.tenant_id = users.tenant_id
      AND ts.active = true
    )
  );
```

### 6. Audit Logging

**Recommendations:**
- Log all auth attempts (success and failure)
- Include token claims (not full token)
- Track role mappings and impersonation
- Use structured logging (JSON)

```go
logger.Info("auth_success",
  "user", claims.Email,
  "roles", claims.Roles,
  "mapped_to_pg", pgUser,
  "client_addr", clientAddr,
  "duration_ms", time.Since(start).Milliseconds(),
)
```

---

## Performance Optimization

### 1. Connection Pool Sizing

**Recommendation for Option 2:**
```
pool_size = (cpu_cores × 2) + spare_connections

Example (4-core server):
- pg_admin:  5 connections (admin overhead)
- pg_writer: 20 connections (most common)
- pg_reader: 10 connections

Total: 35 connections
```

### 2. JWKS Caching Strategy

```
Cache Duration:
- Default: 24 hours (if Auth0 keys don't rotate often)
- Conservative: 1 hour (if keys rotate frequently)
- Aggressive: 7 days (if you control the key rotation schedule)

Background Refresh:
- Refresh JWKS at 80% of TTL (proactive refresh)
- Prevents thundering herd on expiration
- Reduces latency on validation miss
```

### 3. Connection Startup Optimization

**Bottleneck**: Validate JWT + Map role + Connect to PostgreSQL

**Current**: ~50-100ms
- JWT validation: 1-5ms (with JWKS cache)
- Role mapping: <1ms
- PostgreSQL connect: 10-20ms
- Auth negotiation: 30-50ms

**Optimization**:
- Connection pooling reuse: ~2-5ms (much faster!)
- Batch connections to same role
- Implement proxy-level connection keep-alive

---

## Concepts and flows (original `oidc.md`)

## OAuth 2.0 and OIDC: Concepts and Flows

### Overview: OAuth 2.0
OAuth 2.0 is an industry-standard authorization framework that allows a user to grant a third-party application limited access to protected resources without sharing their credentials. It focuses on delegated authorization, not user authentication.

### Problems OAuth 2.0 Addresses
- Credential exposure: Eliminates the need to share usernames/passwords with third parties.
- Scoped access: Grants only the minimal required permissions via scopes.
- Revocation: Enables revoking access for one application without changing the user’s password.

### High-Level OAuth 2.0 Flow
1. The application requests authorization from the user.
2. If granted, the application receives an authorization grant.
3. The application exchanges the grant (plus its own identity) for an access token.
4. If valid, the authorization server issues an access token (and optionally a refresh token).
5. The application calls the resource server with the access token.
6. The resource server validates the token and serves the requested resources.

![OAuth 2.0 overview](https://blog.postman.com/wp-content/uploads/2023/08/OAuth-2.0.png)

### Common Grant Types
- Authorization Code: For server-side applications; most widely used.
- Client Credentials: For machine-to-machine access using the application’s own identity.
- Device Code: For devices without a browser or with limited input capabilities.

---

## Authorization Code Flow (most common)

![Authorization Code flow](https://assets.digitalocean.com/articles/oauth/auth_code_flow.png)

Step 1 — Authorization request link example:
https://cloud.digitalocean.com/v1/oauth/authorize?response_type=code&client_id=CLIENT_ID&redirect_uri=CALLBACK_URL&scope=read

- Authorization endpoint: https://cloud.digitalocean.com/v1/oauth/authorize  
- client_id: The application’s identifier.  
- redirect_uri: Where the service redirects after authorization.  
- response_type=code: Indicates the authorization code flow.  
- scope=read: Requested permission scope.

Step 2 — User authorizes the application  
The user logs in (if needed) and approves or denies access.

![Authorize application prompt](https://assets.digitalocean.com/articles/oauth/authcode.png)

Step 3 — Application receives authorization code  
Example redirect:
https://dropletbook.com/callback?code=AUTHORIZATION_CODE

Step 4 — Application exchanges code for token  
Example token request:
https://cloud.digitalocean.com/v1/oauth/token?client_id=CLIENT_ID&client_secret=CLIENT_SECRET&grant_type=authorization_code&code=AUTHORIZATION_CODE&redirect_uri=CALLBACK_URL

Step 5 — Application receives access token (and optionally refresh token)  
Example response:
{"access_token":"ACCESS_TOKEN","token_type":"bearer","expires_in":2592000,"refresh_token":"REFRESH_TOKEN","scope":"read","uid":100101,"info":{"name":"Mark E. Mark","email":"mark@thefunkybunch.com"}}

---

## Client Credentials Flow
Used when an application needs to access its own resources (no end-user involved).

Example request:
https://oauth.example.com/token?grant_type=client_credentials&client_id=CLIENT_ID&client_secret=CLIENT_SECRET

If valid, the authorization server returns an access token the application can use for its own account.

---

## Device Code Flow
Used by devices without a browser or with limited input.

1. The device requests a device code from the device authorization endpoint.  
   Example:
   POST https://oauth.example.com/device  
   client_id=CLIENT_ID

2. The response includes a device_code, user_code, verification_uri, interval, and expires_in:
{
  "device_code": "IO2RUI3SAH0IQuESHAEBAeYOO8UPAI",
  "user_code": "RSIK-KRAM",
  "verification_uri": "https://example.okta.com/device",
  "interval": 10,
  "expires_in": 1600
}

3. The user visits the verification_uri on another device, enters the user_code, and signs in.
4. Meanwhile, the device polls the token endpoint until the user authorizes or the code expires.
5. On approval, the authorization server returns an access token to the device.

---

## OpenID Connect (OIDC)

### Why OIDC?
OAuth 2.0 provides authorization, not authentication. It does not standardize identity claims, user info, or how to verify who the user is. OIDC adds an identity layer on top of OAuth 2.0 to safely authenticate users.

### What OIDC Adds
- ID Token (JWT) with standardized identity claims (e.g., sub, email, name) and validation fields (iss, aud, exp).
- UserInfo endpoint for additional profile attributes.
- Discovery via well-known configuration.
- The openid scope indicating an authentication request.

In practice, a successful OIDC flow:
- Includes an OAuth 2.0 access token.
- Returns an ID token (JWT) with identity information.
- Uses OAuth 2.0 transport and error semantics.

![OIDC overview](https://awsfundamentals.com/_next/image?url=%2Fassets%2Fblog%2Foidc-introduction%2F2490ce7e-1759-4ebb-8b57-81467fcb21f6.webp&w=3840&q=75)

### OAuth 2.0 vs OIDC (summary)
- Purpose:  
  - OAuth 2.0: Delegate access to resources.  
  - OIDC: Authenticate the user (plus optional resource access).
- Tokens:  
  - OAuth 2.0: Access Token (opaque or JWT).  
  - OIDC: Access Token + ID Token (JWT).
- Standardization:  
  - OAuth 2.0: No standard identity claims.  
  - OIDC: Standard claims and discovery.
- Login use case:  
  - OAuth 2.0 alone: Not sufficient.  
  - OIDC: Safe, standardized login.

Developers started using OAuth tokens as proof of identity (“if I have a token, I must be that user”).
But OAuth never guaranteed identity — the token is just a random opaque string meant for an API.

So, when people tried to use OAuth for authentication (login), it caused:

Inconsistent implementations

Security holes (no standard claims like “email” or “name”)

No standard endpoint to fetch user info

No way to verify the token’s issuer reliably

The Solution: OpenID Connect (OIDC)

OIDC was introduced as a formal identity layer on top of OAuth 2.0.
It adds the missing “who is the user?” piece.

It adds:

ID Token (JWT) → a standardized, cryptographically signed token containing identity info:

sub (subject / user ID)

email, name, etc.

iss, aud, exp for validation

UserInfo endpoint → API to fetch extra user profile data

Standard discovery mechanism → .well-known/openid-configuration endpoint

New scope: openid → signals “I want identity info”

So now, you can safely:

Authenticate the user (via ID Token)

Authorize access to your resource (via Access Token)

If you used only OAuth 2.0, you’d only get:

A token that says, “This client can access the Postgres proxy.”

You wouldn’t know:

Who the user is

What their email or groups are

How to enforce RBAC securely

OIDC adds those missing identity pieces (via ID Token & claims).
---

## PKCE (Proof Key for Code Exchange)

### Why PKCE?
Public clients (native apps and SPAs) cannot safely store a client secret. PKCE strengthens the Authorization Code flow against code interception by binding the authorization request to the token exchange using a one-time verifier.

### How PKCE Works
1. The client creates a cryptographically random code_verifier and derives a code_challenge.
2. The client starts the authorization flow, sending the code_challenge with the authorization request.
3. After the user authenticates and grants consent, the app receives an authorization code.
4. The client exchanges the authorization code for tokens, sending the original code_verifier.
5. The authorization server validates that the code_verifier matches the code_challenge before issuing tokens.

![Authorization Code with PKCE](https://images.ctfassets.net/cdy7uua7fh8z/3pstjSYx3YNSiJQnwKZvm5/33c941faf2e0c434a9ab1f0f3a06e13a/auth-sequence-auth-code-pkce.png)

If Refresh Token Rotation is enabled, a new refresh token is issued with each exchange; the previous one is invalidated while the server maintains linkage for detection and revocation.

---

## Key Takeaways
- Use OAuth 2.0 for delegated authorization (access to resources).
- Use OIDC when you need to authenticate users and obtain standardized identity claims.
- Prefer Authorization Code with PKCE for public clients (native/SPA).
- Use Device Code for limited-input devices; use Client Credentials for machine-to-machine scenarios.
- Rely on discovery documents and JWKS for correct validation of tokens and issuers.

Once the auth0 setup is done refer to the tenant.yaml in  for auth0/tenant.yaml for tenant configurations 

I need to implement OIDC PKCE flow in my proxy when user type gprxy login hence 

Here’s what your CLI must handle:

Generate PKCE parameters

code_verifier → random secure string
Must be a high-entropy cryptographically random string

Length: 43–128 characters

Character set: [A–Z, a–z, 0–9, "-", ".", "_", "~"]

Must be base64url-safe (no =, +, /)

code_challenge → base64(SHA256(code_verifier))

Used to protect against token interception.

state : 

state

A random string used to maintain state between request and callback — for security.

It prevents CSRF (Cross-Site Request Forgery) attacks.

When you start the OAuth flow, your app generates a random state value and remembers it (e.g., in session).
When Auth0 redirects back to your app’s redirect_uri, it includes that same state value.

If they match → it’s a legitimate response.
If not → reject it (could be a malicious redirect).


Build the Auth0 authorization URL
Example:

https://<AUTH0-DOMAIN>/authorize?
    response_type=code&
    code_challenge={codeChallenge}&
    code_challenge_method=S256&
    client_id=<CLIENT-ID>&
    redirect_uri={yourCallbackUrl}&
    scope=SCOPE&
    audience={apiAudience}&
    state={state}


Open a browser window

Launch the URL in the user’s default browser (exec.Command("open", url) on macOS or xdg-open on Linux).

The user logs in (via Azure AD through Auth0).

Start a tiny local webserver on the CLI

Listen on localhost:8085/callback.

Capture the code query parameter when Auth0 redirects the user back after login.

Exchange the code for tokens

Make a POST request to https://<AUTH0_DOMAIN>/oauth/token with:

{
  "grant_type": "authorization_code",
  "client_id": "<CLIENT_ID>",
  "code_verifier": "<code_verifier>",
  "code": "<AUTH_CODE>",
  "redirect_uri": "http://localhost:8085/callback"
}


Auth0 returns:

{
  "access_token": "eyJhbGciOi...",
  "id_token": "eyJhbGciOi...",
  "refresh_token": "eyJhbGciOi...",
  "token_type": "Bearer",
  "expires_in": 86400
}


Save the tokens locally

Store the tokens securely in a local file (e.g. ~/.gprxy/config.json):

{
  "access_token": "...",
  "id_token": "...",
  "refresh_token": "...",
  "expires_at": "2025-10-11T15:00:00Z"
}


Validate and print success

Optionally decode the ID token using JWT library to show:

Logged in as user@email.com (role: admin)


Store minimal info in config.



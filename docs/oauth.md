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
    │  ✓ Signature verification (cached keys)
    │  ✓ Issuer (iss claim) matches config
    │  ✓ Audience (aud claim) matches config
    │  ✓ Expiration (exp claim) not exceeded
    │  ✓ Extract roles from custom claims
    │
    └─ 4. Map to PostgreSQL role and authenticate
        (No network call to Auth0!)
```

---

## Architecture Options

### Option 1: PostgreSQL 18+ Native OAuth (Pass-Through)

**When to use:**
- ✅ PostgreSQL 18+ already deployed
- ✅ Want PostgreSQL as sole authentication authority
- ✅ Minimal custom validation logic needed
- ✅ Already have Auth0/OIDC provider configured

**When NOT to use:**
- ❌ PostgreSQL < 18
- ❌ Complex custom authorization rules needed
- ❌ Need to modify/filter tokens before PostgreSQL sees them
- ❌ Need extensive logging/monitoring at proxy level

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
- ✅ Any PostgreSQL version (13+)
- ✅ Need custom authorization logic
- ✅ Want centralized validation in proxy
- ✅ Need detailed logging/monitoring
- ✅ Want to map multiple OAuth providers to same DB

**When NOT to use:**
- ❌ Token validation becomes bottleneck (unlikely with caching)
- ❌ Proxy unavailable → no new connections (less resilient)

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

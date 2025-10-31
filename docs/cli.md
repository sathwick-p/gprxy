┌─────────────────────────────────────────────────────────────────┐
│ WORKFLOW: Token-Based Authentication with Saved Config         │
└─────────────────────────────────────────────────────────────────┘

Step 1: Initial Authentication (once)
$ gprxy login
→ OAuth flow
→ Save tokens to ~/.gprxy/credentials

Step 2: Connect to database (many times)
$ gprxy connect -h prod-db.com -d myapp
→ Check saved token
→ If valid: connect immediately
→ If expired: auto-refresh or prompt to login

Step 3: Reconnect later (no SSO needed!)
$ gprxy connect -h prod-db.com -d myapp
→ Uses saved token
→ Direct connection (< 1 second)


gprxy
├── login                      # Authenticate via SSO
│   └── (no flags, just OAuth)
│
├── connect                    # Connect to database
│   ├── -h, --host            # Database host (required)
│   ├── -d, --database        # Database name (required)
│   ├── -p, --port            # Database port (default: 5432)
│   ├── -u, --user            # Override username (optional)
│   ├── --profile             # Use saved profile (optional)
│   └── --save-profile        # Save as profile for reuse
│
├── profiles                   # Manage saved profiles
│   ├── list                  # List all saved profiles
│   ├── show <name>           # Show profile details
│   ├── delete <name>         # Delete a profile
│   └── set-default <name>    # Set default profile
│
├── status                     # Show authentication status
│   └── --verbose             # Show token details
│
└── logout                     # Clear credentials
    └── --all                 # Clear all profiles too


~/.gprxy/
├── credentials               # Stores OAuth tokens (sensitive!)
├── config                    # Stores connection profiles (non-sensitive)
└── .gitignore               # Ensure credentials never committed


┌─────────────────────────────────────────────────────────────────┐
│ YOUR ACTUAL USE CASE: Proxy Server in K8s                      │
└─────────────────────────────────────────────────────────────────┘

Developer 1 (Alice)               Developer 2 (Bob)
  │                                     │
  │ psql "host=proxy.svc port=5432     │ psql "host=proxy.svc port=5432
  │       password=<alice_jwt_token>"   │       password=<bob_jwt_token>"
  │                                     │
  └──────────────┬──────────────────────┘
                 │
                 ↓
    ┌────────────────────────────┐
    │  gprxy Proxy (K8s Pod)     │
    │  - Validates each JWT      │
    │  - Maps roles to PG users  │
    │  - Connection pooling      │
    │  - Multi-tenant isolation  │
    └────────────┬───────────────┘
                 │
                 ↓
    ┌────────────────────────────┐
    │  PostgreSQL RDS            │
    │  - Service accounts        │
    │  - RLS policies            │
    └────────────────────────────┘




This section explains how the proxy handles authentication in this OAuth setup.

## Complete Proxy Authentication Flow (No Code)

### **Overview: The Proxy's Role**

The proxy acts as a **trusted intermediary** that:
1. **Accepts** connections from users with JWT tokens
2. **Validates** those tokens locally (cryptographically)
3. **Translates** OAuth identity → PostgreSQL service account
4. **Establishes** traditional PostgreSQL connections
5. **Tracks** user context for auditing and RLS
6. **Proxies** all database traffic bidirectionally

---

## Detailed Step-by-Step Flow

### **Phase 1: User Initiates Connection**

**What happens on user's laptop:**

1. User runs: `gprxy psql -d myapp`
2. CLI tool reads `~/.gprxy/credentials` file
3. Extracts the JWT access token (long string starting with "eyJ...")
4. Checks if token is expired (compares expiry time to now)
5. If expired: automatically calls Auth0's refresh endpoint to get new token
6. If refresh succeeds: saves new token and proceeds
7. If refresh fails: tells user "Please run gprxy login"

**What the CLI sends to proxy:**

8. Opens TCP connection to proxy (e.g., `localhost:5433`)
9. Sends PostgreSQL startup message with:
   - Protocol version: 3.0
   - Database name: "myapp"
   - Username: "alice@company.com" (user's email)
   - Application name: "psql"
10. Proxy responds: "I need your password"
11. CLI sends: "PasswordMessage" containing the JWT token (looks like: `eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCIsImtpZCI6Ik...`)

---

### **Phase 2: Proxy Receives Connection**

**Proxy accepts the connection:**

1. Proxy server is listening on port 5433
2. New TCP connection arrives from user
3. Proxy creates a **Connection object** to track this specific user's session
4. Proxy reads the startup message
5. Extracts: database="myapp", user="alice@company.com"
6. Logs: "Connection request from 192.168.1.5 for user alice@company.com to database myapp"

**Proxy requests credentials:**

7. Proxy sends back: "AuthenticationCleartextPassword" message
8. This tells the client: "Send me your password in plain text"
9. Important: This is safe because the connection is over TLS (encrypted)

**Proxy receives the "password":**

10. Client sends: PasswordMessage with the JWT token
11. Proxy reads the password field
12. Proxy stores this in memory temporarily: `password = "eyJhbGc..."`

---

### **Phase 3: Token Detection**

**Proxy determines authentication type:**

1. Proxy examines the received "password" string
2. Checks: Does it start with "eyJ"?
   - "eyJ" is base64 encoding of `{\"alg\":`
   - All JWTs start with this because of the header format
3. Checks: Is it longer than 100 characters?
   - Regular passwords are typically < 50 chars
   - JWTs are 500-2000 characters

**Decision point:**

4. If starts with "eyJ" AND long → This is a JWT token → Use OAuth flow
5. If not → This is a regular password → Use traditional password authentication
6. Logs: "JWT token detected for user alice@company.com"

---

### **Phase 4: JWT Validation (The Core OAuth Part)**

**Step 1: Parse the JWT structure**

The JWT has three parts separated by dots: `header.payload.signature`

1. Proxy splits the token by "." character
2. Gets three parts: header, payload, signature
3. Each part is base64-url encoded

**Step 2: Decode the header**

4. Proxy base64-decodes the header part
5. Header contains:
   ```
   {
     "alg": "RS256",           ← Signing algorithm (RSA with SHA-256)
     "typ": "JWT",             ← Token type
     "kid": "abc123xyz"        ← Key ID (which Auth0 key was used)
   }
   ```
6. Proxy extracts the "kid" (key identifier): `"abc123xyz"`
7. This tells proxy which public key to use for verification

**Step 3: Get the public key (from cache)**

8. Proxy checks: Do I have Auth0's public keys cached?
9. If cache is empty OR cache is older than 1 hour:
   - Proxy makes HTTP GET request to: `https://test-org-flow.us.auth0.com/.well-known/jwks.json`
   - Auth0 responds with JSON containing multiple public keys
   - Each key has a "kid" field and the key material (N and E values for RSA)
   - Proxy stores this in memory: `cachedKeys = {...}`
   - Proxy notes the time: `cacheTime = now()`
10. If cache exists and fresh: Skip the HTTP request (very fast!)
11. Proxy searches cached keys for one matching `kid = "abc123xyz"`
12. Finds the matching key with modulus (N) and exponent (E)
13. Proxy constructs RSA public key object from N and E values

**Step 4: Verify the signature**

14. Proxy combines: `header.payload` (first two parts of JWT)
15. Proxy computes SHA-256 hash of this combined string
16. Proxy uses RSA public key to decrypt the signature part
17. Compares: Does decrypted signature match the hash?
18. If they match: Token was signed by Auth0 (nobody else could forge this)
19. If they don't match: Token is fake/tampered → Reject connection

**Step 5: Verify the claims**

20. Proxy base64-decodes the payload part
21. Payload contains:
    ```
    {
      "iss": "https://test-org-flow.us.auth0.com/",
      "sub": "waad|aidash-sso|b8972ee5-f637-4037-b59e-99a54d334c94",
      "aud": ["https://gprxy.io", "..."],
      "iat": 1761311296,        ← Issued at (timestamp)
      "exp": 1761397696,        ← Expiration (timestamp)
      "email": "alice@company.com",
      "role": ["write"]
    }
    ```
22. Proxy verifies issuer: Is `iss == "https://test-org-flow.us.auth0.com/"`?
23. Proxy verifies audience: Does `aud` array contain "https://gprxy.io"?
24. Proxy verifies expiration: Is `exp` > current Unix timestamp?
25. If any check fails: Token is invalid → Reject connection

**Step 6: Extract user context**

26. Proxy extracts from verified claims:
    - User ID: `sub = "waad|aidash-sso|b8972ee5..."`
    - Email: `email = "alice@company.com"`
    - Roles: `role = ["write"]`
27. Proxy creates user context object in memory:
    ```
    userContext = {
      userID: "waad|aidash-sso|b8972ee5...",
      email: "alice@company.com",
      roles: ["write"],
      authenticatedAt: now()
    }
    ```
28. Logs: "OAuth authenticated: alice@company.com from 192.168.1.5, roles: [write]"

---

### **Phase 5: Role Mapping**

**Proxy determines which PostgreSQL user to use:**

1. Proxy looks at user's roles: `["write"]`
2. Proxy has a hardcoded mapping table:
   ```
   "admin" → postgres user, password from env var ADMIN_PASSWORD
   "write" → app_writer user, password from env var WRITER_PASSWORD
   "read"  → app_reader user, password from env var READER_PASSWORD
   ```
3. Proxy checks roles in priority order: admin first, then write, then read
4. User has "write" role → Maps to `app_writer`
5. Proxy reads environment variable: `WRITER_PASSWORD = "secure_writer_pass_123"`
6. Logs: "Mapped alice@company.com to PostgreSQL user: app_writer"

**Decision made:**
- PostgreSQL username: `app_writer`
- PostgreSQL password: `secure_writer_pass_123`
- These are **service account credentials**, not the user's credentials

---

### **Phase 6: Connect to PostgreSQL (Traditional Auth)**

**Proxy opens a NEW connection to PostgreSQL:**

1. Proxy opens TCP connection to PostgreSQL server: `postgres.company.com:5432`
2. Sends startup message to PostgreSQL:
   - Database: "myapp"
   - User: "app_writer" (NOT alice@company.com!)
3. PostgreSQL responds: "I need authentication"
4. PostgreSQL checks `pg_hba.conf`: What auth method for app_writer from proxy's IP?
5. `pg_hba.conf` says: "scram-sha-256" (challenge-response authentication)

**SCRAM-SHA-256 handshake:**

6. PostgreSQL sends: "AuthenticationSASL" message with mechanisms: ["SCRAM-SHA-256"]
7. Proxy creates SCRAM client with:
   - Username: "app_writer"
   - Password: "secure_writer_pass_123"
8. Proxy generates random nonce (challenge)
9. Proxy sends: "SASLInitialResponse" with nonce
10. PostgreSQL sends: "AuthenticationSASLContinue" with server nonce and salt
11. Proxy computes proof using password, nonce, salt, and hash functions
12. Proxy sends: "SASLResponse" with computed proof
13. PostgreSQL verifies the proof against stored password hash
14. PostgreSQL sends: "AuthenticationSASLFinal" with server signature
15. Proxy verifies server signature (mutual authentication)
16. PostgreSQL sends: "AuthenticationOk"

**Connection established to PostgreSQL:**

17. PostgreSQL sends: "BackendKeyData" (process ID and secret key for this connection)
18. PostgreSQL sends: "ParameterStatus" messages (server version, encoding, etc.)
19. PostgreSQL sends: "ReadyForQuery" (idle, ready for commands)
20. Proxy now has an authenticated PostgreSQL connection as `app_writer`

---

### **Phase 7: Connection Pooling (Critical for Scale)**

The proxy doesn't create a new PostgreSQL connection every time.

Instead, it uses a **connection pool**:

1. Proxy checks: Do I have a pool for `app_writer@myapp`?
2. If pool exists:
   - Check: Are there idle connections in the pool?
   - If yes: Grab one (reuse existing connection) ← fast
   - If no: Create new connection (max 50 per pool)
3. If pool doesn't exist:
   - Create new pool for `app_writer@myapp`
   - Initial size: 5 connections
   - Max size: 50 connections
4. Proxy acquires connection from pool
5. Connection is now "checked out" to this user's session

**Why this matters:**
- Creating new PostgreSQL connection takes ~50-100ms (TCP + SSL + auth handshake)
- Reusing pooled connection takes ~0.01ms (instant)
- 1000 users → 3 pools (admin, writer, reader) → 100 total DB connections
- Without pooling: 1000 users → 1000 DB connections → Database overload!

---

### **Phase 8: Set Session Context (RLS Setup)**

**Proxy personalizes the pooled connection:**

1. Proxy has a generic `app_writer` connection
2. But we need PostgreSQL to know THIS connection is for Alice
3. Proxy executes SQL commands on the PostgreSQL connection:
   ```
   SET app.user_id = 'waad|aidash-sso|b8972ee5-f637-4037-b59e-99a54d334c94';
   SET app.user_email = 'alice@company.com';
   SET app.user_roles = 'write';
   ```
4. These create **session-level variables** that persist for this connection's lifetime
5. PostgreSQL's RLS policies can now read these variables
6. Every query Alice runs will be filtered by her user_id
7. Logs: "Session context set for alice@company.com"

**Example of how RLS uses this:**
- Alice runs: `SELECT * FROM users;`
- PostgreSQL automatically adds: `WHERE user_id = current_setting('app.user_id')`
- Alice only sees rows where `user_id = 'waad|aidash-sso|b8972ee5...'`
- Bob's data is invisible to Alice!

---

### **Phase 9: Tell Client "You're Authenticated"**

**Proxy sends success messages to the client:**

1. Proxy forwards to client: "AuthenticationOk"
2. Proxy generates fake "BackendKeyData" for the client
   - Uses the pool connection's real PID and secret key
   - Client needs this for cancel requests
3. Proxy forwards: "ParameterStatus" messages
4. Proxy sends: "ReadyForQuery" (idle)
5. Client (psql) now shows: `myapp=>`
6. User can start typing SQL queries

---

### **Phase 10: Query Proxying (Ongoing)**

**For every query the user runs:**

1. Client sends: `Query("SELECT * FROM users")`
2. Proxy receives the query message
3. Proxy forwards it to PostgreSQL connection (the pooled `app_writer` connection)
4. PostgreSQL executes the query with RLS applied (filters by user_id)
5. PostgreSQL sends back: RowDescription, DataRow, DataRow, ..., CommandComplete
6. Proxy forwards all responses to the client
7. Client displays the results to user

**Proxy is completely transparent:**
- User thinks they're talking directly to PostgreSQL
- PostgreSQL thinks it's talking to a normal client
- Proxy sits in the middle, forwarding messages both ways
- But proxy maintains the session context (user identity)

---

### **Phase 11: Connection Cleanup**

**When user disconnects:**

1. Client sends: "Terminate" message (or closes TCP connection)
2. Proxy detects connection closed
3. Proxy executes cleanup on PostgreSQL connection:
   - `ROLLBACK;` (abort any in-flight transaction)
   - `DISCARD ALL;` (clear session state, temp tables, prepared statements)
4. Proxy releases connection back to pool (now idle, ready for reuse)
5. Proxy closes client connection
6. Logs: "Connection closed for alice@company.com, duration: 15m"

The PostgreSQL connection stays alive in the pool.
- Next user with "write" role can reuse this connection
- Proxy will set new session variables for that user
- Connection pooling keeps database efficient

---

## Key Points About the Proxy's Role

### **What the Proxy Does:**

1. **Cryptographic Token Validation**
   - Verifies JWT signature using Auth0's public keys
   - Ensures token hasn't been tampered with
   - Checks expiration and audience claims

2. **Identity Translation**
   - OAuth token (user-specific) → PostgreSQL service account (shared)
   - Maintains audit trail (logs who did what)

3. **Connection Pooling**
   - Reuses database connections efficiently
   - Scales to thousands of users with minimal DB connections

4. **Session Context Management**
   - Sets PostgreSQL variables for each user
   - Enables Row-Level Security (RLS) data isolation

5. **Protocol Translation**
   - Speaks PostgreSQL wire protocol to both client and database
   - Completely transparent to both sides

### **What the Proxy Does NOT Do:**

- **Store user credentials** (stateless, reads from JWT each time)
- **Call Auth0 for every connection** (validates cryptographically with cached keys)
- **Create database users per person** (uses shared service accounts)
- **Modify SQL queries** (just forwards them, RLS happens in PostgreSQL)
- **Store state between requests** (can scale horizontally in Kubernetes)

### **Security Properties:**

- **Defense in Depth**: Multiple layers (JWT validation, PostgreSQL auth, RLS)
- **Zero Trust**: Every connection validated independently
- **Audit Trail**: Full logging of who accessed what
- **Least Privilege**: Users get minimum required permissions
- **Scalability**: Stateless proxy pods can be replicated
- **Isolation**: RLS ensures users can't see each other's data

---

## Why This Design is Powerful

**User Perspective:**
- Simple: Just run `gprxy psql -d mydb`
- Secure: Uses company SSO (Auth0/Azure AD)
- Seamless: Works like normal psql

**Proxy Perspective:**
- Stateless: No user state stored, scales horizontally
- Efficient: Connection pooling reduces DB load
- Secure: Cryptographic validation, no password storage

**Database Perspective:**
- Simple: Only knows about 3 service accounts
- Secure: Only accepts connections from proxy's IP
- Efficient: Few connections, all from trusted source

**Operations Perspective:**
- Observable: Full audit logs of all access
- Scalable: Add more proxy pods as needed
- Maintainable: User management in Auth0, not database

This is why major cloud providers (AWS RDS Proxy, Cloud SQL Proxy, Supabase) use this architecture.


## How the Refresh Token Flow Works

### **Step-by-Step Explanation:**

1. **User runs `gprxy connect`**
   - The `connect()` function is called
   - It calls `getCreds()` to get credentials

2. **`getCreds()` loads credentials from disk**
   - Reads `~/.gprxy/credentials` file
   - Checks `ExpiresAt` timestamp

3. **Token expiry check**
   - If token expires in < 30 minutes: Needs refresh
   - If token expires in > 30 minutes: Use existing token

4. **`getRefreshToken()` is called**
   - Loads old credentials (to get refresh token)
   - Validates refresh token exists

5. **HTTP request to Auth0**
   - POST to `https://test-org-flow.us.auth0.com/oauth/token`
   - Body: `grant_type=refresh_token&client_id=...&refresh_token=...`
   - Auth0 validates the refresh token

6. **Auth0 validates refresh token**
   - Checks if refresh token is valid
   - Checks if it hasn't been revoked
   - Checks if it's within validity period (usually 90 days)

7. **Auth0 returns new tokens**
   - New `access_token` (valid for 24 hours)
   - New `id_token`
   - **May return new `refresh_token`** (rotation for security)

8. **Parse and extract user info**
   - Decode new access token
   - Extract updated roles (in case they changed)
   - Keep user ID, email, name from old credentials (don't change)

9. **Create new credentials object**
   - Store new access token
   - Store new refresh token (if provided)
   - Calculate new expiry time
   - Update roles if changed

10. **Save to disk**
    - Write to `~/.gprxy/credentials`
    - Secure permissions (0600)

11. **Return new credentials**
    - Function returns updated credentials
    - `connect()` uses them to connect to database

### **What Happens in Different Scenarios:**

**Scenario 1: Token still valid (> 30 min left)**
```
getCreds() → loadCreds() → Check expiry → Still valid → Return existing creds
```
No refresh needed. Very fast (< 1ms)

**Scenario 2: Token expiring soon (< 30 min left)**
```
getCreds() → loadCreds() → Check expiry → Expiring soon → getRefreshToken()
→ Auth0 API call → Get new tokens → Save to disk → Return new creds
```
**Refresh happens automatically** (takes ~500ms for API call)

**Scenario 3: Refresh token also expired**
```
getCreds() → loadCreds() → Check expiry → Expiring → getRefreshToken()
→ Auth0 API call → Auth0 returns error "Invalid refresh token"
→ Return error → User sees "Please run 'gprxy login'"
```
**User must re-authenticate with SSO**

**Scenario 4: First time connecting after login**
```
gprxy login → Saves tokens → gprxy connect → getCreds() → loadCreds()
→ Token fresh (23h 59m left) → Return existing creds
```
**No refresh needed**, uses token from login

### **Security Considerations:**

1. **Refresh tokens are long-lived** (90 days typically)
   - If stolen, attacker has access for 90 days
   - Mitigation: Token rotation (Auth0 issues new refresh token)

2. **Access tokens are short-lived** (24 hours)
   - If stolen, only valid for 24 hours
   - Minimizes damage window

3. **Credentials file is secure**
   - Permissions: 0600 (only owner can read/write)
   - Stored in `~/.gprxy/` (user's home directory)

4. **Network security**
   - All Auth0 API calls over HTTPS
   - TLS prevents man-in-the-middle attacks

5. **No password storage**
   - Never stores user password
   - Only stores tokens issued by Auth0

### **Error Handling:**

The function handles these error cases:

1. **No credentials file** → "Not logged in"
2. **No refresh token** → "Please login again"
3. **Auth0 API error** → "Token refresh failed"
4. **Network error** → "Failed to refresh token"
5. **Invalid response** → "Failed to parse response"
6. **Save failure** → Warning, but returns new creds anyway

This ensures robust behavior even in edge cases!

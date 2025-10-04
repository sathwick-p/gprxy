# SCRAM Authentication Implementation

## Table of Contents
1. [The Problem](#the-problem)
2. [Why the Original Approach Failed](#why-the-original-approach-failed)
3. [The Solution](#the-solution)
4. [Authentication Flow](#authentication-flow)
5. [Implementation Details](#implementation-details)
6. [Supported Authentication Methods](#supported-authentication-methods)

---

## The Problem

### Initial State: Transparent Authentication Relay

The original proxy implementation attempted to **transparently relay** authentication messages between the PostgreSQL client (psql) and the PostgreSQL backend server. This approach worked fine for simple authentication methods like MD5, but **completely failed for SCRAM-SHA-256** authentication.

### Symptoms

When attempting to connect using SCRAM-SHA-256 authentication:

```bash
psql "postgresql://testuser_scram@localhost:7777/postgres?sslmode=require"
```

The connection would fail with errors like:
```
FATAL: insufficient data left in message
```

Or the client would send `PasswordMessage` when it should send `SASLInitialResponse`, causing protocol mismatches.

---

## Why the Original Approach Failed

### 1. TLS Channel Binding Problem

SCRAM-SHA-256-PLUS (the preferred variant) uses **TLS channel binding** to tie the authentication to a specific TLS session. This creates a fundamental problem with TLS-terminating proxies:

```
┌────────┐          TLS Session #1          ┌───────┐          TLS Session #2          ┌──────────┐
│ Client │ ←──────────────────────────────→ │ Proxy │ ←──────────────────────────────→ │ Backend  │
└────────┘                                   └───────┘                                   └──────────┘
```

- **TLS Session #1**: Client ↔ Proxy (has channel binding info A)
- **TLS Session #2**: Proxy ↔ Backend (has channel binding info B)

When the client attempts SCRAM-SHA-256-PLUS authentication:
1. Backend advertises: `[SCRAM-SHA-256-PLUS, SCRAM-SHA-256]`
2. Client tries to use SCRAM-SHA-256-PLUS with channel binding from TLS Session #1
3. Backend expects channel binding from TLS Session #2
4. **Mismatch!** Authentication fails or client falls back to wrong method

### 2. Client Library Confusion

The psql client library (libpq) has complex logic for determining authentication methods:
- On the **first connection attempt**, it probes to see what auth is needed
- It then **disconnects** and reconnects with the appropriate credentials
- When receiving modified authentication messages through the proxy, libpq would sometimes:
  - Send `PasswordMessage` instead of `SASLInitialResponse`
  - Get confused about which authentication mechanism to use
  - Cache incorrect authentication method decisions

### 3. Protocol State Management

SCRAM authentication is a multi-step protocol:
```
Backend → Proxy → Client: AuthenticationSASL [mechanisms]
Client → Proxy → Backend: SASLInitialResponse
Backend → Proxy → Client: AuthenticationSASLContinue [challenge]
Client → Proxy → Backend: SASLResponse [proof]
Backend → Proxy → Client: AuthenticationSASLFinal [verification]
Backend → Proxy → Client: AuthenticationOk
```

Simply relaying these messages doesn't work because:
- The client needs to see the exact mechanisms the backend supports
- Any modification to the mechanism list confuses the client
- The proxy can't reliably intercept and modify SASL conversations
- Protocol state becomes inconsistent between client and backend

---

## The Solution

### Paradigm Shift: Proxy as Authentication Mediator

Instead of transparently relaying authentication, **the proxy now performs authentication itself** on behalf of the client. This is the same approach used by the [roundabout PostgreSQL proxy](https://github.com/coder543/roundabout).

```
┌────────┐                              ┌───────┐                              ┌──────────┐
│ Client │                              │ Proxy │                              │ Backend  │
└────────┘                              └───────┘                              └──────────┘
     │                                       │                                       │
     │ 1. StartupMessage                    │                                       │
     │ ─────────────────────────────────────>│                                       │
     │                                       │ 2. StartupMessage                    │
     │                                       │ ─────────────────────────────────────>│
     │                                       │                                       │
     │                                       │ 3. AuthenticationSASL                │
     │                                       │ <─────────────────────────────────────│
     │                                       │    [SCRAM-SHA-256-PLUS, SCRAM-SHA-256]│
     │                                       │                                       │
     │ 4. AuthenticationCleartextPassword   │                                       │
     │ <─────────────────────────────────────│                                       │
     │    (Proxy asks for password)          │                                       │
     │                                       │                                       │
     │ 5. PasswordMessage("scram_password") │                                       │
     │ ─────────────────────────────────────>│                                       │
     │                                       │                                       │
     │                                       │ 6. SASLInitialResponse               │
     │                                       │    (Proxy performs SCRAM)            │
     │                                       │ ─────────────────────────────────────>│
     │                                       │                                       │
     │                                       │ 7. AuthenticationSASLContinue        │
     │                                       │ <─────────────────────────────────────│
     │                                       │                                       │
     │                                       │ 8. SASLResponse                      │
     │                                       │ ─────────────────────────────────────>│
     │                                       │                                       │
     │                                       │ 9. AuthenticationSASLFinal           │
     │                                       │ <─────────────────────────────────────│
     │                                       │                                       │
     │                                       │ 10. AuthenticationOk                 │
     │                                       │ <─────────────────────────────────────│
     │                                       │                                       │
     │ 11. AuthenticationOk                 │                                       │
     │ <─────────────────────────────────────│                                       │
     │                                       │                                       │
     │ 12. Ready to execute queries          │                                       │
```

### Key Insight

The proxy acts as:
- **PostgreSQL server** to the client (asks for password using simple cleartext)
- **PostgreSQL client** to the backend (performs full SCRAM-SHA-256 authentication)

This completely bypasses the channel binding and client library confusion issues.

---

## Authentication Flow

### Detailed Step-by-Step Flow

#### Phase 1: Connection Setup
```go
// File: internal/proxy/startup.go
func (pc *Connection) handleStartupMessage(pgconn *pgproto3.Backend) (*pgproto3.Backend, error) {
    startupMessage, err := pgconn.ReceiveStartupMessage()
    // ... SSL/TLS negotiation if needed ...
    
    user := msg.Parameters["user"]
    database := msg.Parameters["database"]
    
    // Call authentication
    err := auth.AuthenticateUser(user, database, pc.config.Host, msg, pgconn, clientAddr)
}
```

#### Phase 2: Request Password from Client
```go
// File: internal/auth/authenticator.go
func requestPasswordFromClient(clientBackend *pgproto3.Backend, clientAddr string) (string, error) {
    // Send cleartext password request to client
    err := clientBackend.Send(&pgproto3.AuthenticationCleartextPassword{})
    
    // Receive password from client (over secure TLS connection)
    msg, err := clientBackend.Receive()
    passwordMsg := msg.(*pgproto3.PasswordMessage)
    
    return passwordMsg.Password, nil
}
```

**Why cleartext?**
- The client-proxy connection is already secured by TLS
- This simplifies the protocol between client and proxy
- The proxy needs the actual password to perform SCRAM with the backend
- PostgreSQL's wire protocol supports cleartext password authentication

#### Phase 3: Authenticate WITH Backend
```go
func authenticateWithBackend(frontend *pgproto3.Frontend, clientBackend *pgproto3.Backend, 
                            username, password, clientAddr string) error {
    var scramConversation *scram.ClientConversation
    
    for {
        msg, err := frontend.Receive()
        
        switch authMsg := msg.(type) {
        case *pgproto3.AuthenticationSASL:
            // Backend requests SCRAM - proxy acts as SCRAM client
            client, _ := scram.SHA256.NewClient(username, password, "")
            scramConversation = client.NewConversation()
            initialResponse, _ := scramConversation.Step("")
            
            frontend.Send(&pgproto3.SASLInitialResponse{
                AuthMechanism: "SCRAM-SHA-256",
                Data:          []byte(initialResponse),
            })
            
        case *pgproto3.AuthenticationSASLContinue:
            // Continue SCRAM handshake
            response, _ := scramConversation.Step(string(authMsg.Data))
            frontend.Send(&pgproto3.SASLResponse{Data: []byte(response)})
            
        case *pgproto3.AuthenticationSASLFinal:
            // Verify final SCRAM message
            scramConversation.Step(string(authMsg.Data))
            
        case *pgproto3.AuthenticationOk:
            // Success! Forward to client
            clientBackend.Send(&pgproto3.AuthenticationOk{})
            
        case *pgproto3.ReadyForQuery:
            // Forward and return success
            clientBackend.Send(authMsg)
            return nil
        }
    }
}
```

---

## Implementation Details

### File Structure

```
internal/auth/
├── authenticator.go       # Main authentication logic
├── errors.go             # Error handling utilities
├── password.go           # Password auth handlers (legacy, for relay mode)
└── sasl.go              # SASL auth handlers (legacy, for relay mode)
```

### Key Functions

#### 1. `AuthenticateUser()` - Entry Point

```go
func AuthenticateUser(user, database, host string, startUpMessage *pgproto3.StartupMessage, 
                     clientBackend *pgproto3.Backend, clientAddr string) error
```

**Purpose**: Orchestrates the entire authentication process

**Flow**:
1. Opens temporary connection to PostgreSQL backend
2. Sends startup message to backend
3. Requests password from client
4. Performs authentication with backend using the password
5. Forwards success messages back to client
6. Closes temporary connection (client uses pool connection for queries)

**Why temporary connection?**
- Authentication happens once per client connection
- After auth, the client uses a pooled connection for queries
- Temporary connection is discarded after authentication completes

#### 2. `requestPasswordFromClient()` - Get Client Credentials

```go
func requestPasswordFromClient(clientBackend *pgproto3.Backend, clientAddr string) (string, error)
```

**Purpose**: Obtains the user's password from the client

**Protocol**:
```
Proxy → Client: AuthenticationCleartextPassword {}
Client → Proxy: PasswordMessage { password: "user_password" }
```

**Security**: 
- Password transmitted over TLS-encrypted connection
- Only the proxy sees the cleartext password
- Password never logged (except in debug mode)

#### 3. `authenticateWithBackend()` - Backend Authentication

```go
func authenticateWithBackend(frontend *pgproto3.Frontend, clientBackend *pgproto3.Backend, 
                            username, password, clientAddr string) error
```

**Purpose**: Performs actual authentication with PostgreSQL using user's credentials

**Handles**:
- SCRAM-SHA-256 (multi-step challenge-response)
- MD5 password hashing
- Cleartext password
- Error messages
- Success messages (AuthenticationOk, ParameterStatus, BackendKeyData, ReadyForQuery)

---

## Supported Authentication Methods

### 1. SCRAM-SHA-256 (Salted Challenge Response Authentication Mechanism)

**Most secure method** - Uses SHA-256 hashing with salt and multiple iterations.

#### How SCRAM Works

```
Step 1: Client Hello
──────────────────────────────────────────────────────────────
Client → Server: SASLInitialResponse
  - AuthMechanism: "SCRAM-SHA-256"
  - Data: "n,,n=username,r=<client_nonce>"
  
The client sends:
  - Username
  - Random nonce (prevents replay attacks)


Step 2: Server Challenge
──────────────────────────────────────────────────────────────
Server → Client: AuthenticationSASLContinue
  - Data: "r=<client_nonce><server_nonce>,s=<salt>,i=<iterations>"
  
The server sends:
  - Combined nonce (client + server)
  - Salt value (for password hashing)
  - Iteration count (usually 4096)


Step 3: Client Proof
──────────────────────────────────────────────────────────────
Client computes:
  1. SaltedPassword = PBKDF2(password, salt, iterations)
  2. ClientKey = HMAC(SaltedPassword, "Client Key")
  3. StoredKey = SHA-256(ClientKey)
  4. AuthMessage = (initial_msg + "," + server_msg + "," + final_msg)
  5. ClientSignature = HMAC(StoredKey, AuthMessage)
  6. ClientProof = ClientKey XOR ClientSignature

Client → Server: SASLResponse
  - Data: "c=<channel_binding>,r=<nonce>,p=<ClientProof>"


Step 4: Server Verification
──────────────────────────────────────────────────────────────
Server → Client: AuthenticationSASLFinal
  - Data: "v=<ServerSignature>"
  
The server sends:
  - ServerSignature (proves server knows the password)
  
Client verifies the signature to prevent server impersonation.


Step 5: Success
──────────────────────────────────────────────────────────────
Server → Client: AuthenticationOk
```

#### Implementation in Proxy

```go
case *pgproto3.AuthenticationSASL:
    // Create SCRAM client using xdg-go/scram library
    client, err := scram.SHA256.NewClient(username, password, "")
    if err != nil {
        return fmt.Errorf("failed to create SCRAM client: %w", err)
    }
    
    // Start conversation
    scramConversation = client.NewConversation()
    initialResponse, err := scramConversation.Step("")  // Step 1: Client Hello
    
    // Send initial response to backend
    err = frontend.Send(&pgproto3.SASLInitialResponse{
        AuthMechanism: "SCRAM-SHA-256",  // We choose regular SCRAM, not -PLUS
        Data:          []byte(initialResponse),
    })

case *pgproto3.AuthenticationSASLContinue:
    // Step 3: Client Proof
    response, err := scramConversation.Step(string(authMsg.Data))
    err = frontend.Send(&pgproto3.SASLResponse{
        Data: []byte(response),
    })

case *pgproto3.AuthenticationSASLFinal:
    // Step 4: Server Verification
    _, err := scramConversation.Step(string(authMsg.Data))
    if err != nil {
        return fmt.Errorf("SCRAM final step failed: %w", err)
    }
    // Server signature verified, authentication will complete
```

**Why SCRAM-SHA-256 instead of SCRAM-SHA-256-PLUS?**
- `-PLUS` requires TLS channel binding
- Channel binding ties auth to specific TLS session
- Proxy creates two separate TLS sessions (can't use channel binding)
- Regular SCRAM-SHA-256 is still very secure without channel binding

**Security Benefits**:
- Password never sent over network (even encrypted)
- Mutual authentication (client verifies server, server verifies client)
- Resistant to replay attacks (nonces are unique per session)
- Resistant to dictionary attacks (salt + iterations)
- Forward secrecy (nonces change each time)

### 2. MD5 Password Authentication

**Legacy method** - Still widely used, less secure than SCRAM.

#### How MD5 Works

```
Step 1: Server Challenge
──────────────────────────────────────────────────────────────
Server → Client: AuthenticationMD5Password
  - Salt: [4 random bytes]


Step 2: Client Response
──────────────────────────────────────────────────────────────
Client computes:
  1. hash1 = MD5(password + username)
  2. hash2 = MD5(hash1_hex + salt)
  3. result = "md5" + hash2_hex

Client → Server: PasswordMessage
  - Password: "md5a8b7c6d5e4f3..."


Step 3: Server Verification
──────────────────────────────────────────────────────────────
Server performs same calculation and compares hashes

Server → Client: AuthenticationOk (if match)
```

#### Implementation in Proxy

```go
case *pgproto3.AuthenticationMD5Password:
    log.Printf("[%s] backend requests MD5 password", clientAddr)
    
    // Compute MD5(password + username)
    h1 := md5.New()
    io.WriteString(h1, password)
    io.WriteString(h1, username)
    hash1 := fmt.Sprintf("%x", h1.Sum(nil))
    
    // Compute MD5(hash1 + salt)
    h2 := md5.New()
    io.WriteString(h2, hash1)
    h2.Write(authMsg.Salt[:])  // Salt is 4 bytes
    hash2 := fmt.Sprintf("%x", h2.Sum(nil))
    
    // Format as "md5<hash>"
    passwordHash := fmt.Sprintf("md5%s", hash2)
    
    // Send to backend
    err := frontend.Send(&pgproto3.PasswordMessage{Password: passwordHash})
```

**Security Considerations**:
- MD5 is cryptographically broken (collision attacks exist)
- Still better than cleartext
- Vulnerable to rainbow table attacks if salt is known
- PostgreSQL community recommends migrating to SCRAM-SHA-256

### 3. Cleartext Password Authentication

**Least secure method** - Only used when required by configuration.

#### How Cleartext Works

```
Step 1: Server Request
──────────────────────────────────────────────────────────────
Server → Client: AuthenticationCleartextPassword {}


Step 2: Client Response
──────────────────────────────────────────────────────────────
Client → Server: PasswordMessage
  - Password: "actual_password_in_cleartext"


Step 3: Server Verification
──────────────────────────────────────────────────────────────
Server checks password against stored hash

Server → Client: AuthenticationOk (if match)
```

#### Implementation in Proxy

```go
case *pgproto3.AuthenticationCleartextPassword:
    log.Printf("[%s] backend requests cleartext password", clientAddr)
    
    // Simply send the password as-is
    err := frontend.Send(&pgproto3.PasswordMessage{Password: password})
    if err != nil {
        return fmt.Errorf("failed to send cleartext password: %w", err)
    }
```

**When is this used?**
- Backend is configured with `password` auth method in `pg_hba.conf`
- Typically only on trusted networks
- Should always be used with TLS/SSL

**Security Considerations**:
- Password sent in cleartext to backend (even if over TLS)
- Vulnerable if TLS is compromised
- Not recommended for production use

---

## Configuration

### Backend PostgreSQL Configuration

#### Enable SCRAM Authentication

**1. Set password encryption in `postgresql.conf`:**
```conf
password_encryption = 'scram-sha-256'
```

**2. Configure `pg_hba.conf`:**
```conf
# TYPE  DATABASE    USER              ADDRESS         METHOD
host    all         all               0.0.0.0/0       scram-sha-256
host    all         testuser_scram    127.0.0.1/32    scram-sha-256
host    all         testuser_md5      127.0.0.1/32    md5
```

**3. Update user passwords:**
```sql
-- Existing users need password reset after enabling SCRAM
ALTER ROLE testuser_scram WITH PASSWORD 'scram_password';
```

The password will now be stored as a SCRAM hash:
```sql
SELECT rolname, rolpassword 
FROM pg_authid 
WHERE rolname = 'testuser_scram';

-- Output:
-- rolpassword: SCRAM-SHA-256$4096:salt$storedKey:serverKey
```

### Proxy Configuration

No special configuration needed! The proxy automatically:
- Detects the authentication method from the backend
- Performs appropriate authentication
- Supports all methods transparently

---

## Testing

### Test SCRAM-SHA-256 Authentication

```bash
# Start the proxy
go run main.go

# In another terminal, connect through proxy
psql "postgresql://testuser_scram@localhost:7777/postgres?sslmode=require"
Password for user testuser_scram: [enter password]
```

**Expected log output:**
```
[127.0.0.1:xxxxx] connection request - user: testuser_scram, database: postgres
[127.0.0.1:xxxxx] connecting to PostgreSQL at localhost:5432 as testuser_scram
[127.0.0.1:xxxxx] requesting password from client
[127.0.0.1:xxxxx] starting authentication with PostgreSQL backend
[127.0.0.1:xxxxx] backend auth message: *pgproto3.AuthenticationSASL
[127.0.0.1:xxxxx] backend requests SASL auth, mechanisms: [SCRAM-SHA-256-PLUS SCRAM-SHA-256]
[127.0.0.1:xxxxx] sending SCRAM initial response to backend
[127.0.0.1:xxxxx] backend auth message: *pgproto3.AuthenticationSASLContinue
[127.0.0.1:xxxxx] backend SASL continue
[127.0.0.1:xxxxx] backend auth message: *pgproto3.AuthenticationSASLFinal
[127.0.0.1:xxxxx] backend SASL final
[127.0.0.1:xxxxx] backend auth message: *pgproto3.AuthenticationOk
[127.0.0.1:xxxxx] backend authentication OK
[127.0.0.1:xxxxx] authentication completed successfully
```

### Test MD5 Authentication

```bash
psql "postgresql://testuser_md5@localhost:7777/postgres?sslmode=require"
```

**Expected log output:**
```
[127.0.0.1:xxxxx] backend auth message: *pgproto3.AuthenticationMD5Password
[127.0.0.1:xxxxx] backend requests MD5 password
[127.0.0.1:xxxxx] backend authentication OK
```

---

## Advantages of This Approach

### 1. **Protocol Simplicity**
- Client only needs to send password (simple protocol)
- No need for client to support SCRAM
- Works with any PostgreSQL client

### 2. **TLS Channel Binding Resolution**
- Proxy handles SCRAM directly with backend
- No channel binding mismatch issues
- Can use regular SCRAM-SHA-256 (not -PLUS)

### 3. **Flexibility**
- Supports all PostgreSQL authentication methods
- Can add custom authentication logic in future
- Can implement rate limiting, logging, auditing

### 4. **Security**
- Client-proxy connection secured by TLS
- Proxy-backend connection can use strongest auth available
- Password handling centralized in proxy

### 5. **Compatibility**
- Works with old PostgreSQL clients
- Works with new PostgreSQL servers
- No client-side changes needed

---

## Security Considerations

### Password Handling

**The proxy sees cleartext passwords.** This is necessary for SCRAM authentication but requires:

1. **Secure the proxy:**
   - Run in trusted environment
   - Use TLS for client connections
   - Limit access to proxy logs

2. **Never log passwords in production:**
   ```go
   // DEBUG ONLY - remove in production
   log.Printf("Got password: %s", passwordMsg.Password)
   ```

3. **Consider connection pooling security:**
   - Proxy connects to backend with service account
   - Uses `SET ROLE` to impersonate user
   - Requires proper PostgreSQL role configuration

### TLS Requirements

**Always use TLS** between client and proxy:
```bash
psql "postgresql://user@proxy:7777/db?sslmode=require"
```

Without TLS, passwords are sent in cleartext over the network.

### Audit Logging

Consider adding audit logs for authentication attempts:
```go
log.Printf("[AUDIT] User %s authenticated from %s at %s", 
           username, clientAddr, time.Now())
```

---

## Future Enhancements

### 1. Certificate Authentication
Support `cert` auth method from `pg_hba.conf`

### 2. Authentication Caching
Cache authentication results for short periods to reduce backend load

### 3. Custom Authentication
Implement custom auth against LDAP, OAuth, etc., while still using SCRAM with backend

### 4. Connection Pooling with Auth
Pre-authenticate pooled connections, switch roles per client

---

## References

- [PostgreSQL SCRAM-SHA-256 Documentation](https://www.postgresql.org/docs/current/sasl-authentication.html)
- [RFC 5802 - SCRAM Specification](https://datatracker.ietf.org/doc/html/rfc5802)
- [RFC 7677 - SCRAM-SHA-256](https://datatracker.ietf.org/doc/html/rfc7677)
- [Roundabout PostgreSQL Proxy](https://github.com/coder543/roundabout)
- [xdg-go/scram Library](https://github.com/xdg-go/scram)

---

## Conclusion

By shifting from a **transparent relay model** to a **mediator model**, the proxy can successfully handle all PostgreSQL authentication methods, including the complex SCRAM-SHA-256 protocol. The proxy acts as both server (to clients) and client (to PostgreSQL), completely avoiding the TLS channel binding and protocol complexity issues that plagued the original implementation.

This approach is production-ready and used by other PostgreSQL proxies in the wild.


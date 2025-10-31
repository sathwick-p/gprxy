# Changelog — 25/09/25

Project is about trying to build an RDS proxy that sits between the Postgres DB and the client, which might be users/K8s pods.

Currently there are good proxies in the market, but the issue is that the use case I want to solve is either solved by [Teleport](https://github.com/gravitational/teleport), from which I will probably be taking inspiration on how things work and how they are built.

To start off with this I have decided to use Go (current obsession). Go has the usual features of being fast with goroutines, etc.

## `pgx` library

In Go there is a library called `pgx` that handles a lot of low-level development for me:

1. Low-level protocol access
2. High performance
3. In-built connection pooling
4. A lot more mentioned here: [pgx README](https://github.com/jackc/pgx/tree/master?tab=readme-ov-file#features)

## Architecture

```
Client → Proxy → RDS
```

And the basic features I’m trying to implement that any proxy usually has are:

1. Connection pooling
2. Load balancing
3. Reader–write segregation
4. SSO auth with Azure AD via Auth0
5. Auditing and observability on the basis of users from SSO

If all this works and I still have any sort of interest in the project, I would think about implementing more features into it like caching, SQL parsing.

## Bootstrapping the proxy

To start off with my proxy, first the proxy needs to open a TCP connection from the proxy to the DB host based on the input or localhost. I’m listening to the server on port 7777 and once a connection is made, it accepts the connection and sends it to the proxy’s struct where the connection is stored, and then the functions take it upon themselves to handle the connection via goroutines.

Will attach a goroutine ID to track connections — TODO

## PostgreSQL wire protocol

This protocol is basically how the client and server communicate — essentially any user trying to connect to the DB and the DB communicating back to the client.

Every PostgreSQL message has this format:

```
[Message Type (1 byte)] [Length (4 bytes)] [Message Body (Length-4 bytes)]
```

- **Message type**: single character identifying the message
- **Length**: 32-bit int
- **Message body**: the actual data

There are 2 messages here in my proxy to consider:

- **Frontend messages** — sent by client; proxy intercepts these to send to DB
- **Backend messages** — sent by server; proxy intercepts these from the DB and sends to the user

### Startup message

The Startup message in the PostgreSQL wire protocol is the very first message sent when a PostgreSQL connection is established, and it is special and has to be handled separately because:

1. It has no message type
2. It’s always the first message in any PostgreSQL connection
3. It contains the connection parameters like username, DB, application name

Flow:

```
Client → StartupMessage → Your Proxy → StartupMessage → Backend Database
```

So when the first connection is made, the first message sent in the connection is the StartupMessage. I’m handling that separately in a function call. Before calling the startup message handler function, I open a connection to the backend DB.

Once I’m handling the startup message, I first need to get the StartupMessage using the `ReceiveStartupMessage()` call.

One main thing I learnt/realised about interface type switches is that we need to perform a type switch, as `ReceiveStartupMessage()` returns a `pgproto3.FrontendMessage` interface but not a concrete `*pgproto3.StartupMessage`.

So once the startup message is handled, it can also be a request for cancelling a request or even an SSL request, hence the type switch:

```go
switch msg := startupMessage.(type) {
case *pgproto3.StartupMessage:
    // msg is now *pgproto3.StartupMessage
    user := msg.Parameters["user"]
    database := msg.Parameters["database"]

case *pgproto3.SSLRequest:
    // msg is now *pgproto3.SSLRequest
    // Handle SSL negotiation

case *pgproto3.CancelRequest:
    // msg is now *pgproto3.CancelRequest
    processID := msg.ProcessID
    secretKey := msg.SecretKey

default:
    return fmt.Errorf("unknown startup message type: %T", startupMessage)
}
```

Let’s now bring our focus back to the `StartupMessage` type received in the interface via concrete type assertion here.

Once we receive the StartupMessage we can extract the parameters sent by the user like DB name, DB host, and username.

## Authentication

After this, we need to establish the authentication part of the process, as the client needs to be authenticated before giving access to the RDS DB.

There are a couple of ways we can go about this authentication process:

1. Pretending to be the Postgres server in the proxy and just telling the client authentication is successful and you can start sending queries.

   That code would look like this:

   ```go
   // Create AuthenticationOk message
   buf := (&pgproto3.AuthenticationOk{}).Encode(nil)

   // What it is:
   // Message type: 'R' (Authentication response)
   // Subtype: 0 (OK/Success)
   // Meaning: "Authentication successful, you're logged in"
   // Who normally sends it: PostgreSQL server after verifying credentials
   // Who's sending it now: Your proxy (without any verification!)

   // Create ReadyForQuery message and append to buffer
   buf = (&pgproto3.ReadyForQuery{TxStatus: 'I'}).Encode(buf)

   // What it is:
   // Message type: 'Z'
   // TxStatus: 'I' = Idle (not in a transaction)
   // Meaning: "Ready to receive queries"
   // Transaction Status Options:
   // 'I' = Idle (no transaction)
   // 'T' = In transaction block
   // 'E' = Failed transaction block

   // Send both messages to client
   _, err = proxy.clientConn.Write(buf)
   ```

   Flow:

   1. Client → StartupMessage → Your Proxy
   2. Your Proxy → AuthenticationOk → Client (SKIPPING PASSWORD CHECK!)
   3. Your Proxy → ReadyForQuery → Client

   The obvious problems with this approach are the lack of authentication and security issues.

2. What should actually happen (and does happen in most proxies):
   - Transparent pass-through
   - Proxy terminates authentication

### Transparent pass-through

In transparent pass-through, the connection is made to the backend and the message sent by the client is directly forwarded to the backend, and the backend — here Postgres/RDS — handles the authentication via its usual flow of authentication based on the `pg_hba.conf` file.

```go
case *pgproto3.StartupMessage:
    // 1. Connect to backend immediately
    backendConn, err := pc.connectToBackend(msg.Parameters["database"])
    if err != nil {
        return err
    }

    // 2. Forward StartupMessage to backend
    err = backendConn.Send(msg)
    if err != nil {
        return err
    }

    // 3. Let backend handle authentication
    // Forward all auth messages between client and backend
    return pc.forwardAuthenticationFlow(backendConn)
```

```
Client → StartupMessage → Your Proxy → Backend PostgreSQL
Client ← AuthenticationMD5 ← Your Proxy ← Backend PostgreSQL
Client → PasswordMessage → Your Proxy → Backend PostgreSQL
Client ← AuthenticationOK/Error ← Your Proxy ← Backend PostgreSQL
Client ← ReadyForQuery ← Your Proxy ← Backend PostgreSQL
```

### Proxy terminates authentication

This is to add a custom auth layer not supported by the default Postgres authentication methods, like the RDS IAM authentication. If you connect directly to RDS Postgres, the backend itself is doing custom authentication.

If you put a proxy in front of RDS:

- In transparent mode, you’d just forward the CleartextPassword exchange (proxy doesn’t know it’s a token).
- In auth-terminating mode, your proxy would need AWS SDK integration to generate/verify IAM tokens itself.

So for me to build a custom SSO layer on top of the existing authentication, I would have to implement this.

But to get going and started, I want to write a custom proxy with the basic needs fulfilled first — hence going with transparent pass-through for now. Later, I’ll switch back to terminating authentication and then writing custom auth — let’s hope the doc is better for this than `pgproto3`!!

## Auth relay mechanism

Now for the transparent authentication flow I’m going through, next I need to define the auth relay mechanism where the client and server communicate with each other for authentication in multiple steps. To enable this, I would have to write the code handling each case — what to do, etc.

### Complete auth flow example

1. Client connects to my proxy on 7777
2. Client sends a startup message with username/database
3. Proxy connects to backend `psql` on 5432
4. Proxy forwards StartupMessage to backend
5. Backend responds with `AuthenticationMD5Password` — wants password
   - 5.1 This is determined by the `pg_hba.conf` file where the authentication method is defined for each user
   - The PostgreSQL server (backend) determines the authentication method based on:
     - `pg_hba.conf` — PostgreSQL's host-based authentication configuration
     - Username — different users can have different auth methods
     - Client IP/hostname — different sources can have different auth methods
     - Database — different databases can have different auth methods

   Example `pg_hba.conf`:

   ```conf
   # TYPE  DATABASE    USER        ADDRESS         METHOD
   local   all         postgres                    trust
   host    myapp_db    app_user    127.0.0.1/32   md5
   host    analytics   data_user   10.0.0.0/8     scram-sha-256
   host    all         all         0.0.0.0/0      reject
   ```

6. Proxy forwards this auth request to the client
7. Client sends `PasswordMessage` with password hash
8. Proxy forwards this to the backend
9. Backend verifies password against its user DB
10. Backend sends `AuthenticationOK` or `ErrorResponse`
11. Your proxy forwards result to client
12. If successful, backend sends `ReadyForQuery`
13. Your proxy forwards `ReadyForQuery` to client
14. Connection is now ready for SQL queries

Your proxy doesn’t need to know what authentication method will be used because:

- The backend decides based on its configuration
- Your proxy just forwards authentication messages
- The client handles the actual password/auth logic

```go
// 1. Receive StartupMessage from client (NO PASSWORD YET)
startupMsg, err := clientBackend.ReceiveStartupMessage()

// 2. Connect to backend server via TCP
backendConn, err := pc.connectToBackend(database, user)

// 3. Create PostgreSQL protocol frontend
backendFrontend := pgproto3.NewFrontend(reader, writer)

// 4. Forward StartupMessage to backend
err = backendFrontend.Send(startupMsg)

// 5. Backend decides auth method and responds
authMsg, err := backendFrontend.Receive() // Could be AuthenticationMD5, AuthenticationSASL, etc.

// 6. Forward auth request to client
err = clientBackend.Send(authMsg)

// 7. Client responds with password/auth data
passwordMsg, err := clientBackend.Receive() // PasswordMessage, SASLResponse, etc.

// 8. Forward password to backend
err = backendFrontend.Send(passwordMsg)

// 9. Backend verifies and responds
result, err := backendFrontend.Receive() // AuthenticationOK or ErrorResponse

// 10. Forward result to client
err = clientBackend.Send(result)
```

### `relayAuthFlow`

Here comes my `relayAuthFlow` func which is responsible for enabling the client and server to have their conversation about the authentication flow.

Since it is a multistep conversation, it needs to be in a `for` loop as this is not a single request–response.

Example MD5 Authentication (2 exchanges):

```go
// LOOP ITERATION 1:
// Backend → AuthenticationMD5Password → Client
msg = AuthenticationMD5Password{salt: [1, 2, 3, 4]}
cb.Send(msg) // Forward to client
// needsClientResponse(msg) = true, so:
clientMsg = cb.Receive() // Get PasswordMessage from client
bf.Send(clientMsg)       // Forward password to backend
// Continue loop...

// LOOP ITERATION 2:
// Backend → AuthenticationOK → Client
msg = AuthenticationOK{}
cb.Send(msg) // Forward to client
// Continue loop...

// LOOP ITERATION 3:
// Backend → BackendKeyData → Client
msg = BackendKeyData{ProcessID: 12345, SecretKey: 67890}
cb.Send(msg) // Forward to client
// Continue loop...

// LOOP ITERATION 4:
// Backend → ParameterStatus → Client (server_version)
msg = ParameterStatus{Name: "server_version", Value: "13.7"}
cb.Send(msg) // Forward to client
// Continue loop...

// LOOP ITERATION 5:
// Backend → ParameterStatus → Client (DateStyle)
msg = ParameterStatus{Name: "DateStyle", Value: "ISO, MDY"}
cb.Send(msg) // Forward to client
// Continue loop...

// LOOP ITERATION 6:
// Backend → ReadyForQuery → Client
msg = ReadyForQuery{TxStatus: 'I'}
cb.Send(msg) // Forward to client
// This hits the return nil case - AUTHENTICATION COMPLETE!
return nil // EXIT the function
```

```
Client connects → handleStartupMessage() → relayAuthFlow()
                                              ↓
                  ┌─────────────────────────────────────┐
                  │         AUTHENTICATION LOOP         │
                  │                                     │
                  │ 1. Backend → AuthRequest → Client   │
                  │ 2. Client → Password → Backend      │
                  │ 3. Backend → AuthOK → Client        │
                  │ 4. Backend → BackendKeyData → Client│
                  │ 5. Backend → ParameterStatus → Client│
                  │ 6. Backend → ReadyForQuery → Client │ ←── EXIT LOOP
                  └─────────────────────────────────────┘
                                              ↓
                              Authentication COMPLETE
                                              ↓
                         Enter Query Handling Loop
                                              ↓
                  ┌─────────────────────────────────────┐
                  │          QUERY LOOP                 │
                  │                                     │
                  │ 1. Client → Query → Backend         │
                  │ 2. Backend → Results → Client       │
                  │ 3. Backend → ReadyForQuery → Client │
                  │ 4. Repeat for next query...         │
                  └─────────────────────────────────────┘
```

The loop terminates on:

- A success — case where it is `ReadyForQuery`
- A failure — terminates on `pgproto3.ErrorResponse`
- A connection error — when the proxy is unable to read from the backend

Your `sendErrorToClient` function should only be used for proxy-specific errors:

```go
// Use this ONLY when YOUR PROXY has an error
// (can't connect to backend, internal proxy error, etc.)
return pc.sendErrorToClient(pgconn, "Backend server unavailable")

// For backend responses (errors or success), just forward:
err = clientBackend.Send(backendResponse) // Backend handles formatting
```

Once this auth flow is completed, we have successfully handled the `StartupMessage` part where we have enabled the user to either successfully log in or show an error for being unable to log in.

Now, the other 2 cases in the type switch we did for `ReceiveStartupMessage()` are pending, which are the SSL request and `CancelRequest`, both of which I would be implementing in the upcoming days as I wanted to proceed with queries and be able to at least finish the full flow of the proxy — user logging in and querying the DB.

## Query handling

For handling the queries we have a separate function call from `handleStartupMessage`, which is `handleMessage`.

Now this function is triggered after successful completion of authentication and, once this is done, the function has a receive call from the client to listen to the client queries/messages.

Similarly, as we did for authentication with simple type switching, here we have to get a concrete implementation of the interface of `func Receive() (pgproto3.FrontendMessage, error)`. `Receive` receives a message from the frontend. The returned message is only valid until the next call to `Receive`.

Currently we have separated it out, but there is no need to do so as we are not doing any custom implementation or logic here except for printing log statements on which user ran which query and what queries they did, etc. We are using the extended query protocol here.

Unlike the simple query protocol, the extended query protocol splits execution into steps (Parse → Bind → Execute → Sync), allowing reuse and more control.

1. **Parse**
   - Creates a prepared statement from a query
   - Can be named (persists for session) or unnamed (replaced on next parse)
   - Only one SQL statement allowed per parse
2. **Bind**
   - Uses a prepared statement to create a portal (execution context)
   - Provides actual parameter values and result format (text/binary)
   - Named portals live until the transaction ends; unnamed portals are replaced on the next bind
3. **Execute**
   - Runs a portal, optionally fetching only a limited number of rows
   - Can return `CommandComplete`, `Error`, `EmptyQueryResponse`, or `PortalSuspended` (if partial fetch)
4. **Sync**
   - Marks the end of request cycle
   - Commits or rollbacks the transaction
   - Always results in `ReadyForQuery` response ensuring the client and server resync after errors

### Message types

| Message Type | Purpose                                                     |
|--------------|-------------------------------------------------------------|
| QUERY        | Executes SQL directly in one message (simple protocol)      |
| PARSE        | Prepares SQL statement with parameter placeholders for reuse |
| DESCRIBE     | Gets metadata (columns, types) about statements or portals   |
| BIND         | Binds actual parameter values to a prepared statement       |
| EXECUTE      | Runs a bound statement and returns results                  |
| SYNC         | Forces server to process all pending commands and send responses |
| TERMINATE    | Client gracefully closes the database connection            |

Once this is handled, the only thing left to do is to send the message from frontend to the backend and also handle termination of connection, then go into another function of getting backend responses to the frontend, which is basically the query executed’s results or errors, etc., to the client in the frontend — `relayBackendResponse()`.

```go
func (pc gprxyConn) relayBackendResponse(client *pgproto3.Backend) error {
    for {
        // Get response from the backend
        msg, err := pc.bf.Receive() // ← Backend sends result messages
        if err != nil {
            return fmt.Errorf("Backend receive error: %w", err)
        }

        err = client.Send(msg) // ← HERE: Forward ALL results to client
        if err != nil {
            return fmt.Errorf("Client send error: %w", err)
        }
        // ...
    }
}
```

Here there is also type switching for 3 types of messages: `pgproto3.ReadyForQuery`, `pgproto3.ErrorResponse`, `pgproto3.CommandComplete`.

```
Client → handleMessage() → Backend → relayBackendResponse() → Client

┌─────────────────────────────────────────┐
│ handleConnection() - Main Loop          │
│                                         │
│  for {                                  │
│    handleMessage()      ←── ONE CYCLE   │
│      ↓                                  │
│    relayBackendResponse()               │
│      ↓                                  │
│    return to loop                       │
│  }                                      │
└─────────────────────────────────────────┘
```

# Changelog - 26/09/25

Today's plan: get connection pooling in, plus SSL and cancel request handling. Looked easy (pgxpool exists), turned out not-so-easy. If you're following along, here's what I ran into.

### Quick refresher: what `pgxpool` gives you

- It lives inside your Go process.
- It maintains a pool of physical connections to Postgres.
- When you `pool.Acquire(ctx)`, you get a dedicated backend connection from the pool.
- You hold that connection until you `Release()` it.
- During that time, no other goroutine can use that same physical connection.

One catch: `pgxpool` doesn't do transactional pooling — you get a physical connection only for the duration of a transaction; after that it goes back to the pool.

- After `COMMIT` / `ROLLBACK`, the backend connection is returned to the pool.
- The next client transaction might reuse the same backend.
- Or statement pooling is pooling for each statement.

So the pooling that `pgxpool` provides is basically a client-side pooling.

Since I'm using goroutines, I want a single shared pool per DB for the whole process so each goroutine doesn't spin up its own. One pool per DB, reused everywhere.

First, set some sane pool config (max conns, timeouts, etc.). Right now I'm using:

```go
const defaultMaxConns = int32(7)
const defaultMinConns = int32(0)
const defaultMaxConnLifetime = time.Hour
const defaultMaxConnIdleTime = time.Minute * 30
const defaultHealthCheckPeriod = time.Minute
const defaultConnectTimeout = time.Second * 5
```

Then, since we can have multiple goroutines talking to a single connection pool, our logic has to be such that the first connection pool is created by the first goroutine that is started, and after that all the goroutines need to use the same connection pool and cannot create their own.

To make this safe, I use an RWMutex so multiple readers are fast and only pool creation takes an exclusive lock.

I keep a pool of connections per database name (`poolManager[database]`). Multiple goroutines can ask for a pool at the same time. I don't want duplicate pools, and I don't want to lock too aggressively either.

The pool manager is basically a `map[string]*pgxpool.Pool` that will get created for each DB:

```go
poolManager := map[string]*pgxpool.Pool{
    "postgres":  pool1,  // Pool for 'postgres' database
    "myapp_db":  pool2,  // Pool for 'myapp_db' database
    "analytics": pool3,  // Pool for 'analytics' database
}
```

#### How I'm locking it (double-checked)

- Step 1: Try fast read with `RLock`

```go
poolMutex.RLock()
pool, exists := poolManager[database]
poolMutex.RUnlock()

if exists {
    return pool, nil
}
```

Fast path: `RLock` lets multiple goroutines read at once. If the pool already exists, we just return. Super quick once pools are warmed up.

- Step 2: Acquire write lock

```go
poolMutex.Lock()
defer poolMutex.Unlock()
```

If the pool doesn’t exist, we take the write lock and create it. Only one goroutine can be here at a time, and `defer` makes sure we always unlock.

- Step 3: Double-check (avoid duplicate work)

```go
if pool, exists := poolManager[database]; exists {
    return pool, nil
}
```

Why double-check? Because another goroutine might have won the race and created the pool while we were waiting. The second check avoids duplicate work.

- Step 4: Safe creation

```go
pool = pgxpool.New(...)
poolManager[database] = pool
return pool, nil
```

Now we’re the only goroutine holding the write lock. Safe to create a new pool and store it in the map. After this, all future calls will find it at Step 1.

### Quick recap: `pgxpool` internals

By default:

- `min_conns = 0`
- `max_conns = 4` (unless you set `pool_max_conns` in the connection string)

When you `pool.Acquire(ctx)`, it either:

- Hands you an existing connection from the pool (if available), or
- Creates a new connection (if under `max_conns`), or
- Waits until a connection is free (if at capacity).

So, verifying it means proving:

- Multiple goroutines share fewer DB connections than goroutines.
- Connections are reused instead of re-created each time.

Once this is done, the pool is created successfully. When the backend needs to connect to the Postgres server, it needs to open a connection to the DB, and this is done via the `AcquireConnection()` where it acquires a connection via the `Acquire()` function from `pgxpool` and returns that connection to the connect function.

### Temporary vs pooled connections for auth

When the proxy needs to connect to the backend, there’s a wrinkle because of a race you can hit.

The proxy needs service-account creds to connect and build the pool.

- Pool creates connection → Authenticates as `GPRXY_USER`
- But the issue is that when you use `pgxpool`, the connection is already authenticated and established at the pool level using your service user credentials. But then you're trying to run the authentication flow again with the client's credentials over an already-authenticated connection.

What I do instead is:

- Pool connections authenticate as a service user
- The proxy forwards the authentication to PostgreSQL and handles it properly
- If auth succeeds and since it's the first goroutine, it creates a pool

Flow:

- Client connects → "I'm alice with password xyz123"
- Proxy creates temp connection → Forwards to PostgreSQL
- PostgreSQL validates → "Password correct" or "Password wrong"
- If auth succeeds → Proxy gets pooled connection for queries
- If auth fails → Connection rejected

The code shape looks like this:

```go
// 1. Create a TEMPORARY connection for authentication
tempConn, err := pc.createTempConnection(database, user)
if err != nil {
    return pc.sendErrorToClient(pgconn, "Database unavailable")
}
defer tempConn.Close()

// 2. Run FULL authentication flow with PostgreSQL
err = pc.AuthenticateUser(pgconn, tempConn, msg)
if err != nil {
    return err // Authentication failed
}

// 3. ONLY after successful auth, get pooled connection
err = pc.connectBackend(database, user)
if err != nil {
    return pc.sendErrorToClient(pgconn, "Database unavailable")
}

// 4. Set up for query forwarding
underlyingConn := pc.poolConn.Conn().PgConn().Conn()
pc.bf = pgproto3.NewFrontend(pgproto3.NewChunkReader(underlyingConn), underlyingConn)
```

If/when I add custom authentication later, the first auth step would look like:

```go
// AWS RDS Proxy approach - custom authentication
func (pc *gprxyConn) authenticateWithIAM(user, token string) error {
    // Validate IAM token
    return aws.ValidateIAMToken(user, token)
}
```

So the service user I have set up currently is the `postgres` user directly, which is the superuser, but ideally it should be a user following least-privilege principle.

### Debugging auth failure and SASL

After wiring this up, I started testing — and ran into this:

I was getting this error log saying:

```
received from PostgreSQL: *pgproto3.AuthenticationSASL
unknown message type: *pgproto3.AuthenticationSASL
waiting for client response
forwarding client response: *pgproto3.PasswordMessage
received from PostgreSQL: *pgproto3.ErrorResponse
PostgreSQL auth error: insufficient data left in message
```

And with this the password auth failed for the `postgres` user multiple times, whereas I'm able to connect normally without the proxy.

Digging deeper, I understood and found out that there is an encryption method that is used by the Postgres DB to encrypt the passwords and this by default is `scram-sha-256`.

- PostgreSQL 17 uses SCRAM-SHA-256 (SASL) by default, not MD5
- Your proxy didn't recognize `AuthenticationSASL` as a valid message type
- Client was sending wrong response format for SASL authentication

So yes, I need to add proper SASL handling; the backend will do its thing once I speak the right protocol.

To verify this, I created a dummy user to check if it would work as expected:

```sql
SET password_encryption = 'md5';
CREATE USER testuser WITH PASSWORD 'testpass';
GRANT ALL PRIVILEGES ON DATABASE postgres TO testuser;
```

And lo and behold, when I connect with this:

```bash
psql "postgresql://testuser@localhost:7777/postgres"
```

the password auth worked fine and I was in the DB without any errors. So now we know that this was the actual issue.

The difference is in when and how the passwords were created:

- `postgres` user: Created with SCRAM-SHA-256 password hash (before we changed settings)
- `testuser`: Created with MD5 password hash (after we set `password_encryption = 'md5'`)

Even though `SHOW password_encryption` shows `scram-sha-256` for both users, that's the current setting, not how their existing passwords are stored.

So I have two options: default everyone to MD5 (not acceptable), or add SASL support. Obviously I’m going with adding SASL support.

But the real issue isn’t Postgres — it’s the client ↔ proxy exchange. Here’s what’s actually happening:

- PostgreSQL → Proxy: "Use SCRAM-SHA-256 authentication"
- Proxy → Client: Forwards the SASL request
- Client → Proxy: Sends simple `PasswordMessage` (wrong!)
- Proxy → PostgreSQL: Forwards the wrong message type
- PostgreSQL: "This isn't a SASL message!" → Error

So we had to intercept the client's password message and convert it to proper SASL messages — I'm fully confused right now, will continue this tomorrow.

---

# Changelog - 27/09/25

Here’s what I’m seeing right now:

1. Client "postgres" authenticates →  Authentication succeeds
2. Proxy gets pooled connection as "testuser" → Connection established
3. Client runs: `SELECT * FROM users` → Executed as "testuser" 
4. Database sees: "testuser ran this query" (not "postgres")

### Why this matters

- Authentication: Client proves they are "postgres"
- Authorization: All queries run as "testuser"
- Audit logs: Show "testuser" performed all actions
- Row-level security: Applied based on "testuser", not "postgres"

### The Complete Flow — What's Actually Happening

#### Phase 1: Client Authentication (Temporary Connection)

```
1. Client connects as "testuser" → Proxy
2. Proxy → PostgreSQL (temp connection): "I'm testuser with password"
3. PostgreSQL → Proxy: "testuser authenticated successfully"
4. Proxy → Client: "Authentication OK"
5. Temp connection closed
```

#### Phase 2: Service User Connection (Pool Connection)

```
1. Proxy calls connectBackend()
2. Uses config.BuildConnectionString() → "postgres://postgres:1234@localhost:5432/cloudfront_data"
3. Pool creates connection as "postgres" (service user)
4. All subsequent queries use this service user connection
```

### What’s actually broken here

#### Issue 1: User Identity Confusion

```go
// In your logs, you see:
parameter status - session_authorization: testuser
```

Why this bites: both the client and service user are "testuser", so you can't see the separation.

#### Issue 2: No Real User Identity Preservation

```go
// Line 141: Client user stored for logging only
pc.user = user // Store user for logging

// Lines 188, 191, etc: Queries logged with client user
log.Printf("[%s] [%s] QUERY: %s", clientAddr, pc.user, query.String)
```

Effect: queries are logged as the client user but executed as the service user.

#### Issue 3: Authentication vs Operation Identity Mismatch

When different users connect:

```
Client "postgres" → Authenticates as "postgres" 
But queries execute as "testuser" (service user) 
```

### Detailed Service User Usage Trace

#### Step-by-Step Service User Flow

1. Startup: `config.Load()` reads `GPRXY_USER=testuser` from `.env`
2. Client connects: Authentication happens with client credentials (temporary)
3. Backend connection: `connectBackend()` called
4. Connection string built: `postgres://testuser:testpass@localhost:5432/cloudfront_data`
5. Pool connection: All connections created as `testuser`
6. Query execution: All SQL runs as `testuser`

#### Evidence from Your Logs

```
Created a new connection pool for database: cloudfront_data  ← Service user pool
[localhost] acquired connection from pool for database: cloudfront_data  ← Service user connection
Pool stats - Total: 1, Acquired: 1, Idle: 0  ← Service user pool stats
```

Short version: `pgxpool` does not preserve the client user’s identity. All queries run under the database user in the pool’s connection string.

So:

- The Go client → connects to `pgxpool` with one database user/password (e.g. `postgres` or `app_user`).
- Every query executed through `pgxpool` runs as that same user.
- The application user identity (who triggered the query) is not automatically visible to PostgreSQL.

Why?

- `pgxpool` is client-side pooling (inside your Go app).
- It keeps a pool of backend connections already authenticated as the configured DB user.
- When your app needs a connection, it borrows one from the pool — no new DB login/auth happens.
- This makes pooling fast, but it means Postgres only ever sees one user identity (the one in the DSN).

#### Comparison with Other Approaches

- `pgbouncer` in transaction pooling: Same — you lose per-app user identity. Everything runs as the configured user.
- `pgbouncer` in session pooling: Preserves session user identity, but still usually one DB user for performance.
- RDS Proxy with IAM auth: Can map IAM identities to DB users, so the DB might see different users per connection.

#### What happens today (`pgxpool` default)

You initialized your pool with one DSN (e.g. `postgres://proxy_user:secret@localhost:5432/postgres`). Every connection in that pool is logged in as `proxy_user`. No matter who your client is, when a query goes to Postgres it’s run as `proxy_user`. That’s why user identities aren’t preserved automatically.

If you run `pgxpool` in the default mode, the proxy user is the only identity that PostgreSQL will see. That means:

- This user must have all the privileges required by the client queries you expect to relay.
- Clients themselves are authenticated at the proxy level, not at Postgres.
- The proxy user effectively acts as a service account or super-app user.

So, for example:

- If your proxy serves users that only read from tables → the proxy user must have `SELECT` on those tables.
- If your proxy needs to insert/update/delete → it needs `INSERT/UPDATE/DELETE`.
- If you want it to “act on behalf” of other users → it needs `SET ROLE` or `SET SESSION AUTHORIZATION` rights.
- If the proxy should bypass all restrictions → make it a superuser (not recommended for production).

In short: the proxy user needs the union of all permissions required by the workloads it will serve.

That’s not okay for my setup — I’ve separated client users and DBs, and compliance won’t love a single super-powered proxy user.

When a client connects through your proxy, their actual Postgres username/password is not used anymore. Instead, your proxy process opens connections to Postgres using one fixed role (the proxy user).

This means:

- All queries on the database are executed as the proxy user.
- Any per-user database permissions/separation you had before is lost.
- If your proxy user has access to multiple databases/schemas, then by definition, the proxy process can query them — even if the original client shouldn’t.

So yes, you’re right: unless you implement extra checks in your proxy code, your proxy user can “see too much.”

### How folks handle this in prod

- Application-level separation
  - You enforce which database/schema a given client can touch inside your proxy.
  - Example: client connects with their app credentials → your proxy maps them to a certain DB/schema → proxy user only runs queries in that scope.
  - Downside: Postgres itself no longer enforces per-user security — you’re responsible.

- Multiple pools (per database/role)
  - Instead of a single proxy user with global rights, you spin up a `pgxpool` per client role.
  - Each pool connects to Postgres with the real DB user credentials.
  - Then you keep the permission separation intact, but at the cost of more pools and more connections.

- Use a connection pooler that supports transaction-pooling + auth (like PgBouncer in `auth_user` mode)
  - PgBouncer can preserve client identity for authentication and enforce role separation, even while pooling.

Given all that, my plan: create DB pools per user. In my k8s setup, ~12 pods use the same DB user and each might make one connection — per-user pools still give me solid pooling benefits without losing identity.

I might even make this a toggle later (flag-driven) and support both modes.

### Why creating a pool per user is better

- Preserves database-level permissions
  - Each pool connects using the actual client’s DB user credentials.
  - Postgres enforces that user’s access automatically — no need for your proxy to implement extra checks.
  - If `user1` only has access to `db1`, they cannot touch `db2` even though the proxy exists.

- Transparency
  - Postgres sees the real client identity in `pg_stat_activity`, logs, and RLS policies.
  - Easier to audit.

- Safety
  - Even if there’s a bug in your proxy, the proxy can’t escalate privileges because it’s not using a superuser or global pool account.

#### Trade-offs

- Resource usage
  - Each pool maintains connections. If you have many users, you can quickly exhaust Postgres’s `max_connections`.
  - Example: 100 users × 10 connections per pool = 1,000 backend connections.

- Complexity
  - You need to manage a map of pools keyed by username.
  - Handle creation, reuse, and cleanup of pools dynamically.

- Idle connections
  - You may have idle connections for users who aren’t active.
  - Could be mitigated with proper pool config (`MaxConns`, `MinConns`, idle timeout).

### SCRAM vs MD5 — the exact issue and solution direction

Alright, here’s the exact SCRAM vs MD5 issue I ran into and how I’m approaching it.

#### The Issue: SCRAM-SHA-256 vs MD5 Authentication Mismatch

##### What's Happening in Your Logs

```
connection request - user: testuser3, database: cloudfront_data, app: psql
connecting to PostgreSQL at localhost:5432 for authentication
starting authentication relay
authentication failed - severity: FATAL, code: 08P01, message: insufficient data left in message
```

##### Root Cause Analysis

1. Password Storage Format

When you created `testuser3` without setting `password_encryption = 'md5'`:

```sql
-- This creates SCRAM-SHA-256 password hash
CREATE USER testuser3 WITH PASSWORD 'testpass3';
```

Result: Password stored as `SCRAM-SHA-256$4096:...` (not `md5...`).

2. Authentication Method Selection

PostgreSQL determines authentication method based on stored password format:

```
testuser3 password → SCRAM-SHA-256 format → PostgreSQL sends AuthenticationSASL
```

3. Protocol Mismatch

Your proxy's authentication flow:

```
1. PostgreSQL → Proxy: "Use SCRAM-SHA-256 authentication" (AuthenticationSASL)
2. Proxy → Client: Forwards SASL request
3. Client → Proxy: Sends PasswordMessage (simple password)
4. Proxy → PostgreSQL: Forwards PasswordMessage
5. PostgreSQL: "This isn't a SASL message!" → Error: "insufficient data left in message"
```

#### Detailed Technical Explanation

##### The Protocol Flow Breakdown

Expected SASL Flow:

```
PostgreSQL → AuthenticationSASL(mechanisms: [SCRAM-SHA-256])
Client → SASLInitialResponse(mechanism: SCRAM-SHA-256, data: client_first_message)
PostgreSQL → AuthenticationSASLContinue(server_first_message)
Client → SASLResponse(client_final_message)
PostgreSQL → AuthenticationSASLFinal(server_final_message)
PostgreSQL → AuthenticationOk
```

What Actually Happens:

```
PostgreSQL → AuthenticationSASL(mechanisms: [SCRAM-SHA-256])
Client → PasswordMessage(password: "testpass3")  ← WRONG MESSAGE TYPE!
PostgreSQL → ErrorResponse("insufficient data left in message")
```

Why "insufficient data left in message"?

PostgreSQL expects a SASL message structure but receives a simple password message. The SASL parser tries to read SASL-specific fields that don't exist in a `PasswordMessage`, causing the parsing error.

So it’s either “switch to MD5 globally” (nope) or “implement SASL properly.” I’m going with SASL.

Quick look at what my code actually does right now and why it fails:

#### Current Code Analysis

##### What my code currently does

Your `relayAuthFlow` function follows this pattern:

```
1. Read message from PostgreSQL → msg
2. IMMEDIATELY forward msg to client → cb.Send(msg)
3. Check if client response needed → needsClientResponse(msg)
4. If yes, get client response and forward to PostgreSQL
```

##### The Problem: Message Order Issue

Here’s the sequence that’s blowing up:

```
1. PostgreSQL → Proxy: AuthenticationSASL{mechanisms: ["SCRAM-SHA-256"]}
2. Proxy → Client: AuthenticationSASL (forwarded immediately)
3. Client → Proxy: PasswordMessage("testpass3") ← CLIENT SENDS WRONG MESSAGE TYPE!
4. Proxy → PostgreSQL: PasswordMessage("testpass3")
5. PostgreSQL: "Expected SASLInitialResponse, got PasswordMessage" → ERROR!
```

##### Why This Happens

- The `psql` client behavior:
  - When `psql` receives `AuthenticationSASL`, it should send `SASLInitialResponse`.
  - But `psql` sometimes sends `PasswordMessage` instead (especially with simple password prompts).
  - The proxy blindly forwards whatever the client sends.

- PostgreSQL's expectation:
  - PostgreSQL sent `AuthenticationSASL` → expects `SASLInitialResponse`.
  - It gets `PasswordMessage` instead → tries to parse it as SASL → "insufficient data left in message".

##### The Core Issue: No Protocol Translation

Right now I’m just a transparent relay — forwarding messages as-is without understanding or translating them.

###### What I have

```go
// Blind forwarding
if needsClientResponse(msg) {
    clientMsg, err := cb.Receive()  // Could be PasswordMessage
    // ...
    err = bf.Send(clientMsg)        // Forwards PasswordMessage to PostgreSQL
}
```

###### What I need

```go
// Protocol-aware handling
switch pgMsg := msg.(type) {
case *pgproto3.AuthenticationSASL:
    // Handle SASL specifically
    clientMsg := getClientResponse()

    switch clientResponse := clientMsg.(type) {
    case *pgproto3.PasswordMessage:
        // Convert to SASLInitialResponse
        saslMsg := convertPasswordToSASL(clientResponse.Password)
        bf.Send(saslMsg)  // Send converted message
    case *pgproto3.SASLInitialResponse:
        // Forward as-is
        bf.Send(clientMsg)
    }
}
```

##### Why I need to split authentication types

Right now, `needsClientResponse()` treats all auth types the same:

```go
case *pgproto3.AuthenticationMD5Password,      // Expects: PasswordMessage
     *pgproto3.AuthenticationCleartextPassword, // Expects: PasswordMessage
     *pgproto3.AuthenticationSASL,              // Expects: SASLInitialResponse ← DIFFERENT!
     *pgproto3.AuthenticationSASLContinue,      // Expects: SASLResponse ← DIFFERENT!
     *pgproto3.AuthenticationSASLFinal:         // Expects: No response ← DIFFERENT!
    return true
```

Reality: Different auth types expect different message types:

| PostgreSQL Sends | Client Should Send | Your Code Forwards |
|------------------|-------------------|--------------------|
| `AuthenticationMD5Password` | `PasswordMessage` | `PasswordMessage` |
| `AuthenticationSASL` | `SASLInitialResponse` | `PasswordMessage` |
| `AuthenticationSASLContinue` | `SASLResponse` | `PasswordMessage` |
| `AuthenticationSASLFinal` | (Nothing) | Waits for response |

##### The Message Discrepancy Source

In your logs:

```
authentication failed - severity: FATAL, code: 08P01, message: insufficient data left in message
```

What PostgreSQL is doing (simplified):

```c
// PostgreSQL's SASL message parser (simplified)
if (msgtype == 'p') {  // SASLInitialResponse expected
    mechanism = read_string(msg);     // Tries to read mechanism name
    data_len = read_int32(msg);       // Tries to read data length
    data = read_bytes(msg, data_len); // Tries to read SASL data
    // ERROR: PasswordMessage doesn't have this structure!
}
```

PasswordMessage structure:

```
PasswordMessage: ['p'] [length] [password_string] [null_terminator]
```

SASLInitialResponse structure:

```
SASLInitialResponse: ['p'] [length] [mechanism_string] [null] [data_length] [sasl_data]
```

PostgreSQL tries to parse `PasswordMessage` as `SASLInitialResponse` → structure mismatch → parsing error!

##### Why Your Current SASL Support Doesn't Work

You have `AuthenticationSASL` in `needsClientResponse()`, but:

1. No message type checking — you don't verify what the client actually sends.
2. No protocol translation — you forward `PasswordMessage` when PostgreSQL expects `SASLInitialResponse`.
3. No SASL conversation handling — SASL has multiple round trips (Initial → Continue → Final).

##### The Solution

You need protocol-aware authentication handling instead of blind message forwarding:

```go
switch authType := msg.(type) {
case *pgproto3.AuthenticationSASL:
    return handleSASLAuth(cb, bf, authType)  // SASL-specific logic
case *pgproto3.AuthenticationMD5Password:
    return handleMD5Auth(cb, bf, authType)   // MD5-specific logic
}
```

This is why the split implementation is necessary — each authentication method has different message expectations and conversation flows that require specific handling logic. The current code assumes all auth methods work the same way (send request → get password → forward), but SASL is fundamentally different (multi-step conversation with specific message types).

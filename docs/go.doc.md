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
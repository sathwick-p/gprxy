# Cancel Request Implementation

## Table of Contents
1. [Overview](#overview)
2. [PostgreSQL Cancel Request Protocol](#postgresql-cancel-request-protocol)
3. [Architecture](#architecture)
4. [Implementation Details](#implementation-details)
5. [Code Walkthrough](#code-walkthrough)
6. [Detailed Flow Diagram](#detailed-flow-diagram)
7. [Key Challenges and Solutions](#key-challenges-and-solutions)
8. [Testing](#testing)

---

## Overview

The cancel request feature allows PostgreSQL clients to interrupt long-running queries by sending a special cancel request message. This document details how our proxy implements this functionality while maintaining connection pooling and proper query cancellation.

### What is a Cancel Request?

A cancel request is a special PostgreSQL protocol message that allows a client to interrupt a query that's currently running on the backend. When a user presses `Ctrl+C` in `psql` or calls `pg_cancel_backend()`, the client sends a cancel request to terminate the executing query.

### Why is it Complex in a Proxy?

In a direct PostgreSQL connection, the client has:
1. The backend's Process ID (PID)
2. The backend's Secret Key

These are sent in the `BackendKeyData` message during connection startup. The client uses these to identify which backend process to cancel.

In our proxy with connection pooling:
- **Authentication** happens on a temporary connection
- **Query execution** happens on a pooled connection
- The client needs the **pooled connection's** PID/Key, not the temporary auth connection's

---

## PostgreSQL Cancel Request Protocol

### BackendKeyData Message

After successful authentication, PostgreSQL sends a `BackendKeyData` message containing:
```
{
    ProcessID: <backend_process_id>,
    SecretKey: <random_secret>
}
```

The client caches these values for potential future cancellation.

### CancelRequest Message

When the client wants to cancel a query, it:
1. Opens a **new TCP connection** to the server
2. Sends a special 16-byte message:
   ```
   Bytes 0-3:   Length (16)
   Bytes 4-7:   Cancel request code (80877102)
   Bytes 8-11:  Process ID
   Bytes 12-15: Secret Key
   ```
3. Closes the connection immediately

**Important**: The cancel request does **not** use the normal PostgreSQL message protocol. It's a raw binary message.

---

## Architecture

### Components Involved

```
┌─────────────────────────────────────────────────────────────┐
│                         Client (psql)                        │
│  - Caches BackendKeyData (PID, SecretKey)                  │
│  - Opens new connection for cancel requests                 │
└─────────────────────────────────────────────────────────────┘
                          │
                          │ CancelRequest
                          ▼
┌─────────────────────────────────────────────────────────────┐
│                     Proxy Server                             │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  Connection Registry (Map)                           │  │
│  │  Key: uint64(PID << 32 | SecretKey)                 │  │
│  │  Value: *Connection                                  │  │
│  └──────────────────────────────────────────────────────┘  │
│                          │                                   │
│                          │ Lookup                            │
│                          ▼                                   │
│  ┌──────────────────────────────────────────────────────┐  │
│  │  Target Connection                                    │  │
│  │  - Has pooled backend connection                     │  │
│  │  - Has backend PID/SecretKey                         │  │
│  └──────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
                          │
                          │ Forward CancelRequest
                          ▼
┌─────────────────────────────────────────────────────────────┐
│                  PostgreSQL Backend                          │
│  - Receives raw cancel request                              │
│  - Validates PID and SecretKey                              │
│  - Terminates the running query                             │
└─────────────────────────────────────────────────────────────┘
```

### Key Design Decisions

1. **Connection Registry**: Server maintains a map of active connections indexed by `(PID, SecretKey)`
2. **Pooled Connection Keys**: Client receives BackendKeyData from the **pooled** connection, not the temp auth connection
3. **Separate Cancel Connection**: Cancel requests arrive on a new connection, separate from the query connection
4. **Raw Binary Protocol**: Cancel requests bypass the normal pgproto3 message encoding

---

## Implementation Details

### 1. Server Structure with Connection Registry

**File**: `internal/proxy/server.go`

```go
type Server struct {
    config            *config.Config
    tlsConfig         *tls.Config
    activeConnections map[uint64]*Connection  // Connection registry
    connMutex         sync.RWMutex            // Thread-safe access
}
```

#### Registry Key Generation
```go
// Combines ProcessID and SecretKey into a single uint64
// High 32 bits = ProcessID, low 32 bits = SecretKey
func (s *Server) makeCancelKey(processId, secretKey uint32) uint64 {
    return (uint64(processId) << 32) | uint64(secretKey)
}
```

**Why combine into uint64?**
- Efficient map key (single value vs tuple)
- Fast comparison and hashing
- Unique identifier for each connection

#### Registry Operations

**Register Connection**:
```go
func (s *Server) registerConnection(processId, secretkey uint32, conn *Connection) {
    s.connMutex.Lock()
    defer s.connMutex.Unlock()
    key := s.makeCancelKey(processId, secretkey)
    s.activeConnections[key] = conn
    logger.Debug("registered connection: PID=%d, secret_key=%d, map_key=%d", 
                 processId, secretkey, key)
}
```

**Unregister Connection** (on disconnect):
```go
func (s *Server) unregisterConnection(processId, secretkey uint32, conn *Connection) {
    s.connMutex.Lock()
    defer s.connMutex.Unlock()
    key := s.makeCancelKey(processId, secretkey)
    delete(s.activeConnections, key)
}
```

**Lookup Connection** (for cancel):
```go
func (s *Server) getConnectionForCancelRequest(processId, secretkey uint32) (*Connection, bool) {
    s.connMutex.RLock()
    defer s.connMutex.RUnlock()
    key := s.makeCancelKey(processId, secretkey)
    conn, exists := s.activeConnections[key]
    return conn, exists
}
```

### 2. Connection Structure

**File**: `internal/proxy/connection.go`

```go
type Connection struct {
    conn      net.Conn
    config    *config.Config
    poolConn  *pgxpool.Conn           // Pooled backend connection
    bf        *pgproto3.Frontend       // Backend frontend
    user      string
    db        string
    tlsConfig *tls.Config
    server    *Server                  // Reference to server (for registry)
    key       *pgproto3.BackendKeyData // Backend PID and SecretKey
}
```

The `key` field stores the **pooled connection's** BackendKeyData, which is what the client needs for cancellation.

### 3. Startup Sequence - Sending Correct BackendKeyData

**File**: `internal/proxy/startup.go`

The critical part is ensuring the client receives the **pooled connection's** PID/Key, not the temporary authentication connection's.

#### Step 1: Authentication (Temp Connection)
```go
// Authenticate using temporary connection
keyData, err := auth.AuthenticateUser(user, database, pc.config.Host, msg, pgconn, clientAddr)
if err != nil {
    return nil, err
}
// keyData contains temp connection's PID/Key (NOT sent to client)
```

In `internal/auth/authenticator.go`, the temp connection's BackendKeyData is **intercepted**:
```go
case *pgproto3.BackendKeyData:
    logger.Debug("temp auth connection backend key data received (will not forward): PID=%d, secret_key=%d", 
                 authMsg.ProcessID, authMsg.SecretKey)
    // Store but DO NOT forward to client
    *backendKeyData = authMsg
    continue
```

#### Step 2: Pool Connection Setup
```go
// Establish pooled backend connection
err = pc.connectBackend(database, user)

// Get the POOL connection's PID and SecretKey
backendPID := pc.poolConn.Conn().PgConn().PID()
backendSecretKey := pc.poolConn.Conn().PgConn().SecretKey()

pc.key = &pgproto3.BackendKeyData{
    ProcessID: uint32(backendPID),
    SecretKey: uint32(backendSecretKey),
}
```

#### Step 3: Send Pool Connection's BackendKeyData to Client
```go
logger.Debug("pool connection backend key: PID=%d, secret_key=%d", backendPID, backendSecretKey)

// Send the POOL connection's BackendKeyData to client
err = pgconn.Send(pc.key)
if err != nil {
    logger.Error("failed to send BackendKeyData to client: %v", err)
    return nil, fmt.Errorf("failed to send backend key data")
}
logger.Debug("sent pool BackendKeyData to client")
```

#### Step 4: Send ReadyForQuery
```go
// Complete the startup sequence
readyMsg := &pgproto3.ReadyForQuery{TxStatus: 'I'} // 'I' = idle
err = pgconn.Send(readyMsg)
if err != nil {
    logger.Error("failed to send ReadyForQuery to client: %v", err)
    return nil, fmt.Errorf("failed to send ready for query")
}
```

#### Step 5: Register in Connection Registry
```go
if pc.key != nil && pc.server != nil {
    pc.server.registerConnection(pc.key.ProcessID, pc.key.SecretKey, pc)
    logger.Debug("registered connection: PID=%d, secret_key=%d",
                 pc.key.ProcessID, pc.key.SecretKey)
}
```

### 4. Cancel Request Handling

**File**: `internal/proxy/startup.go`

When a cancel request arrives (on a new connection):

```go
case *pgproto3.CancelRequest:
    logger.Info("cancel request received: PID=%d, secret_key=%d", 
                msg.ProcessID, msg.SecretKey)
    
    // Look up the target connection in the registry
    targetConn, exists := pc.server.getConnectionForCancelRequest(
        msg.ProcessID, msg.SecretKey)
    
    if !exists {
        logger.Warn("cancel request for unknown connection")
        return nil, fmt.Errorf("cancel request processed - connection unknown")
    }
    
    logger.Debug("found target connection: user=%s, db=%s", 
                 targetConn.user, targetConn.db)
    
    // Forward the cancel request to the backend
    err := cancelRequest(pc.config.Host, msg)
    if err != nil {
        logger.Error("failed to forward cancel request: %v", err)
        return nil, fmt.Errorf("cancel request failed: %w", err)
    }
    
    logger.Info("cancel request forwarded successfully")
    return nil, fmt.Errorf("cancel request processed")
```

### 5. Forwarding Cancel Request to Backend

**File**: `internal/proxy/connection.go`

The `cancelRequest` function sends the raw binary cancel request to PostgreSQL:

```go
func cancelRequest(host string, cancel *pgproto3.CancelRequest) error {
    // Open a new connection to the backend
    backendAddr := fmt.Sprintf("%s:5432", host)
    conn, err := net.DialTimeout("tcp", backendAddr, 5*time.Second)
    if err != nil {
        return fmt.Errorf("failed to connect to backend: %w", err)
    }
    defer conn.Close()
    
    // Build the 16-byte cancel request message
    buf := make([]byte, 16)
    
    // Bytes 0-3: Message length (16 bytes)
    binary.BigEndian.PutUint32(buf[0:4], 16)
    
    // Bytes 4-7: Cancel request code (80877102)
    binary.BigEndian.PutUint32(buf[4:8], 80877102)
    
    // Bytes 8-11: Process ID
    binary.BigEndian.PutUint32(buf[8:12], cancel.ProcessID)
    
    // Bytes 12-15: Secret key
    binary.BigEndian.PutUint32(buf[12:16], cancel.SecretKey)
    
    // Send raw bytes to backend
    _, err = conn.Write(buf)
    if err != nil {
        return fmt.Errorf("failed to send cancel: %w", err)
    }
    
    logger.Debug("cancel request forwarded to backend: PID=%d, secret_key=%d",
                 cancel.ProcessID, cancel.SecretKey)
    return nil
}
```

**Key points**:
- Opens a **new** TCP connection (not the pooled connection)
- Sends **raw binary** data (not using pgproto3 encoding)
- Uses correct byte order (Big Endian)
- Closes connection immediately after sending

### 6. Connection Cleanup

**File**: `internal/proxy/connection.go`

When a connection closes, it's automatically unregistered:

```go
func (pc *Connection) handleConnection() {
    logger.Debug("new client connection established")
    pgc := pgproto3.NewBackend(pgproto3.NewChunkReader(pc.conn), pc.conn)
    
    defer func() {
        // ... close connection and release pool ...
        
        // Unregister from connection registry
        if pc.key != nil and pc.server != nil {
            pc.server.unregisterConnection(pc.key.ProcessID, pc.key.SecretKey, pc)
        }
        
        logger.Info("connection closed")
    }()
    
    // ... handle queries ...
}
```

---

## Code Walkthrough

Let's trace a complete flow from connection establishment through query cancellation.

### Scenario: User Runs Long Query and Cancels It

#### Phase 1: Connection Establishment

1. **Client connects to proxy**
   ```
   Client → Proxy: TCP connection established
   ```

2. **TLS negotiation** (if enabled)
   ```
   Client → Proxy: SSLRequest
   Proxy → Client: 'S' (accept)
   [TLS Handshake]
   ```

3. **Client sends StartupMessage**
   ```go
   // startup.go - handleStartupMessage()
   case *pgproto3.StartupMessage:
       user := msg.Parameters["user"]
       database := msg.Parameters["database"]
       logger.Info("connection request - user: %s, database: %s", user, database)
   ```

4. **Proxy authenticates with temporary connection**
   ```go
   // Temp connection created in auth.AuthenticateUser()
   tempConnection, err := net.DialTimeout("tcp", backendAddress, 10*time.Second)
   
   // SCRAM authentication performed
   // BackendKeyData received: PID=12345, SecretKey=98765
   // BUT NOT SENT TO CLIENT!
   
   case *pgproto3.BackendKeyData:
       logger.Debug("temp auth connection backend key data received (will not forward): PID=%d", 
                    authMsg.ProcessID)
       *backendKeyData = authMsg  // Store but don't forward
       continue
   ```

5. **Proxy establishes pooled connection**
   ```go
   // startup.go
   err = pc.connectBackend(database, user)
   
   // Get pool connection's PID/Key
   backendPID := pc.poolConn.Conn().PgConn().PID()          // PID=67890
   backendSecretKey := pc.poolConn.Conn().PgConn().SecretKey() // Key=54321
   
   pc.key = &pgproto3.BackendKeyData{
       ProcessID: uint32(backendPID),
       SecretKey: uint32(backendSecretKey),
   }
   ```

6. **Send pool's BackendKeyData to client**
   ```go
   // This is the CRITICAL step!
   err = pgconn.Send(pc.key)  // Sends PID=67890, Key=54321
   logger.Debug("sent pool BackendKeyData to client")
   
   // Send ReadyForQuery
   readyMsg := &pgproto3.ReadyForQuery{TxStatus: 'I'}
   err = pgconn.Send(readyMsg)
   ```

7. **Register connection in registry**
   ```go
   pc.server.registerConnection(pc.key.ProcessID, pc.key.SecretKey, pc)
   // Registry now contains: {(67890, 54321) → *Connection}
   ```

#### Phase 2: Query Execution

8. **Client sends query**
   ```go
   // message.go - handleMessage()
   case *pgproto3.Query:
       logger.Info("[%s] query: %s", pc.user, query.String)
       // Query: "SELECT pg_sleep(60);"
   ```

9. **Proxy forwards to backend**
   ```go
   err = pc.bf.Send(msg)  // Send to pooled backend connection
   ```

10. **Backend starts executing query**
    ```
    Backend Process 67890: Running "SELECT pg_sleep(60);"
    ```

#### Phase 3: User Presses Ctrl+C

11. **Client's psql receives SIGINT**
    ```
    User: ^C (Ctrl+C)
    psql: Interrupt signal received
    psql: Looking up cached BackendKeyData: PID=67890, Key=54321
    ```

12. **Client opens NEW connection for cancel**
    ```
    Client → Proxy: New TCP connection established
    ```

13. **Client sends CancelRequest** (no TLS/SSL negotiation for cancel)
    ```go
    // startup.go - handleStartupMessage()
    case *pgproto3.CancelRequest:
        logger.Info("cancel request received: PID=%d, secret_key=%d", 
                    msg.ProcessID, msg.SecretKey)
        // Received: PID=67890, Key=54321
    ```

14. **Proxy looks up target connection**
    ```go
    targetConn, exists := pc.server.getConnectionForCancelRequest(
        msg.ProcessID,    // 67890
        msg.SecretKey)    // 54321
    
    // server.go - getConnectionForCancelRequest()
    key := s.makeCancelKey(67890, 54321)  // = 291538189635889
    conn, exists := s.activeConnections[key]  // Found!
    ```

15. **Proxy forwards cancel to backend**
    ```go
    err := cancelRequest(pc.config.Host, msg)
    
    // connection.go - cancelRequest()
    // Opens new connection to PostgreSQL
    conn, err := net.DialTimeout("tcp", "localhost:5432", 5*time.Second)
    
    // Builds 16-byte message:
    // [0-3]:   16
    // [4-7]:   80877102
    // [8-11]:  67890
    // [12-15]: 54321
    
    buf := make([]byte, 16)
    binary.BigEndian.PutUint32(buf[0:4], 16)
    binary.BigEndian.PutUint32(buf[4:8], 80877102)
    binary.BigEndian.PutUint32(buf[8:12], 67890)
    binary.BigEndian.PutUint32(buf[12:16], 54321)
    
    conn.Write(buf)  // Send to PostgreSQL
    conn.Close()     // Immediately close
    ```

16. **PostgreSQL receives and processes cancel**
    ```
    PostgreSQL Backend: Received cancel request
    PostgreSQL Backend: Validating PID=67890, Key=54321
    PostgreSQL Backend: Match found! Sending SIGINT to process 67890
    Process 67890: Query interrupted, sending ErrorResponse
    ```

17. **Query connection receives error**
    ```go
    // message.go - relayBackendResponse()
    case *pgproto3.ErrorResponse:
        logger.Warn("query error: %s (code: %s)",
                    msgType.Message, msgType.Code)
        // Message: "canceling statement due to user request"
        // Code: "57014"
    ```

18. **Error forwarded to client**
    ```
    Proxy → Client: ErrorResponse
    Proxy → Client: ReadyForQuery (status: 'I' = idle)
    ```

19. **Client displays error**
    ```
    psql: ERROR: canceling statement due to user request
    psql: Ready for next query
    ```

#### Phase 4: Connection Cleanup (when client disconnects)

20. **Client closes connection**
    ```
    Client → Proxy: Terminate message
    ```

21. **Proxy cleans up**
    ```go
    // connection.go - handleConnection() defer block
    pc.server.unregisterConnection(pc.key.ProcessID, pc.key.SecretKey, pc)
    // Registry entry removed: {(67890, 54321) → *Connection}
    
    pc.poolConn.Release()  // Return to pool
    pc.conn.Close()        // Close client connection
    ```

---

## Detailed Flow Diagram

### Complete Cancel Request Flow

```
┌─────────────────┐
│     Client      │
│    (psql)       │
└────────┬────────┘
         │
         │ 1. StartupMessage
         │
         ▼
┌────────────────────────────────────────┐
│         Proxy - handleStartupMessage   │
└────────┬───────────────────────────────┘
         │
         │ 2. Authenticate (temp connection)
         │    BackendKeyData: PID=12345, Key=98765
         │    [INTERCEPTED - NOT SENT TO CLIENT]
         │
         │ 3. Create pool connection
         │
         ▼
┌────────────────────────────────────────┐
│    Pool Connection Established         │
│    PID: 67890                          │
│    SecretKey: 54321                    │
└────────┬───────────────────────────────┘
         │
         │ 4. Send BackendKeyData to client
         │    (PID=67890, Key=54321)
         │
         │ 5. Send ReadyForQuery
         │
         │ 6. Register in registry
         │    Map[makeCancelKey(67890,54321)] = *Connection
         │
         ▼
┌────────────────────────────────────────┐
│  Client caches: PID=67890, Key=54321   │
└────────┬───────────────────────────────┘
         │
         │ 7. Query: SELECT pg_sleep(60);
         │
         ▼
┌────────────────────────────────────────┐
│    Backend Process 67890               │
│    Executing long query...             │
└────────┬───────────────────────────────┘
         │
         │ 8. User presses Ctrl+C
         │
         ▼
┌─────────────────┐
│     Client      │
│  Opens NEW conn │ ───────┐
└─────────────────┘         │
                            │ 9. CancelRequest
                            │    (PID=67890, Key=54321)
                            │
                            ▼
                   ┌────────────────────────────────┐
                   │  Proxy - handleStartupMessage  │
                   │  case *CancelRequest:          │
                   └────────┬───────────────────────┘
                            │
                            │ 10. Lookup in registry
                            │     Key = makeCancelKey(67890, 54321)
                            │
                            ▼
                   ┌────────────────────────────────┐
                   │  Connection Registry           │
                   │  Found: *Connection            │
                   └────────┬───────────────────────┘
                            │
                            │ 11. cancelRequest()
                            │
                            ▼
                   ┌────────────────────────────────┐
                   │  New TCP connection to         │
                   │  PostgreSQL                    │
                   └────────┬───────────────────────┘
                            │
                            │ 12. Send raw 16-byte message
                            │     [16][80877102][67890][54321]
                            │
                            ▼
                   ┌────────────────────────────────┐
                   │    PostgreSQL Backend          │
                   │    Validates PID/Key           │
                   │    Sends SIGINT to process     │
                   └────────┬───────────────────────┘
                            │
                            │ 13. ErrorResponse
                            │     "canceling statement..."
                            │
                            ▼
                   ┌────────────────────────────────┐
                   │  Proxy - relayBackendResponse  │
                   │  Forwards error to client      │
                   └────────┬───────────────────────┘
                            │
                            ▼
                   ┌────────────────────────────────┐
                   │         Client                 │
                   │  Displays: ERROR: canceling... │
                   │  Ready for next query          │
                   └────────────────────────────────┘
```

---

## Key Challenges and Solutions

### Challenge 1: Wrong PID Sent to Client

**Problem**: Initially, the client received the **temporary authentication connection's** PID/Key, but queries were executed on the **pooled connection** with a different PID/Key.

**Solution**: 
1. Intercept the temp connection's `BackendKeyData` (don't forward to client)
2. After establishing pool connection, send the **pool's** `BackendKeyData` to client
3. Register the pool's PID/Key in the connection registry

### Challenge 2: ReadyForQuery Timing

**Problem**: `ReadyForQuery` was being sent during authentication, before the pool connection was established.

**Solution**:
1. Intercept `ReadyForQuery` from temp connection
2. Send `ReadyForQuery` only **after** sending the pool's `BackendKeyData`

### Challenge 3: Binary Protocol for Cancel Request

**Problem**: Cancel requests use a special 16-byte binary format, not the standard PostgreSQL message protocol.

**Solution**:
- Manually construct the 16-byte buffer using `encoding/binary`
- Send raw bytes directly to backend
- Use a new TCP connection (not the pooled connection)

### Challenge 4: Connection Registry Thread Safety

**Problem**: Multiple goroutines accessing the connection registry simultaneously.

**Solution**:
- Use `sync.RWMutex` for thread-safe access
- Read lock for lookups (multiple concurrent readers)
- Write lock for register/unregister (exclusive access)

### Challenge 5: TLS Handling for Cancel Requests

**Problem**: Cancel requests should **not** go through TLS negotiation.

**Solution**:
- Cancel requests are handled in `handleStartupMessage` before any TLS setup
- The `cancelRequest` function opens a plain TCP connection to backend
- PostgreSQL's cancel protocol doesn't use TLS

---

## Testing

### Manual Testing with psql

1. **Start the proxy**:
   ```bash
   LOG_LEVEL=debug ./gprxy
   ```

2. **Connect and run long query**:
   ```bash
   psql "postgresql://testuser@localhost:7777/postgres?sslmode=require"
   ```
   ```sql
   SELECT pg_sleep(60);
   ```

3. **Press Ctrl+C** while query is running

4. **Expected logs** (DEBUG mode):
   ```
   [INFO] connection request - user: testuser, database: postgres
   [DEBUG] temp auth connection backend key data received (will not forward): PID=12345
   [DEBUG] pool connection backend key: PID=67890, secret_key=54321
   [DEBUG] sent pool BackendKeyData to client
   [DEBUG] registered connection: PID=67890, secret_key=54321
   [INFO] [testuser] query: SELECT pg_sleep(60);
   [INFO] cancel request received: PID=67890, secret_key=54321
   [DEBUG] found target connection: user=testuser, db=postgres
   [DEBUG] cancel request forwarded to backend: PID=67890, secret_key=54321
   [INFO] cancel request forwarded successfully
   [WARN] query error: canceling statement due to user request (code: 57014)
   ```

5. **Expected client output**:
   ```
   postgres=> SELECT pg_sleep(60);
   ^CCancel request sent
   ERROR:  canceling statement due to user request
   postgres=>
   ```

### Automated Testing

```go
// Test scenario
func TestCancelRequest(t *testing.T) {
    // 1. Establish connection
    conn, err := pgx.Connect(context.Background(), 
        "postgresql://testuser@localhost:7777/postgres")
    require.NoError(t, err)
    defer conn.Close(context.Background())
    
    // 2. Start long-running query in goroutine
    ctx, cancel := context.WithCancel(context.Background())
    errChan := make(chan error)
    
    go func() {
        _, err := conn.Exec(ctx, "SELECT pg_sleep(30)")
        errChan <- err
    }()
    
    // 3. Wait a bit for query to start
    time.Sleep(100 * time.Millisecond)
    
    // 4. Cancel the context (triggers cancel request)
    cancel()
    
    // 5. Verify query was canceled
    err = <-errChan
    require.Error(t, err)
    assert.Contains(t, err.Error(), "canceling statement")
}
```

---

## Future Enhancements

1. **Metrics**: Track cancel request counts and success rates
2. **Timeout Configuration**: Make cancel timeout configurable
3. **Cancel History**: Log cancel request history for debugging
4. **Health Checks**: Periodic validation of connection registry integrity

---

## References

- [PostgreSQL Wire Protocol Documentation](https://www.postgresql.org/docs/current/protocol-flow.html#PROTOCOL-FLOW-CANCELING-REQUESTS)
- [pgproto3 Library](https://github.com/jackc/pgproto3)
- [Connection Pooling with pgxpool](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool)

---

## Summary

The cancel request implementation in our proxy handles the complexity of:
1. **Dual Connection Model**: Separating authentication (temp) from execution (pool)
2. **Correct PID Distribution**: Ensuring client has the pooled connection's PID/Key
3. **Connection Registry**: Mapping cancel requests to active connections
4. **Binary Protocol**: Properly formatting and forwarding raw cancel messages
5. **Thread Safety**: Protecting the connection registry with appropriate locking

This allows clients to cancel long-running queries seamlessly, even though queries execute on pooled connections that differ from the authentication connection.

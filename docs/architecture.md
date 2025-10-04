# 🏗️ **Complete RDS Proxy Architecture & Code Flow Analysis**

## **🎯 Overview: Dual-Connection Architecture**

Your proxy uses a **dual-connection architecture**:
1. **Temporary Connection**: For client authentication (disposable)
2. **Pooled Connection**: For query execution (reusable, service user)

---

# 📋 **Step-by-Step Code Flow**

## **Phase 1: Server Initialization**

### **1.1 Configuration Loading (`main.go` → `config.Load()`)**
```go
// main.go
cfg := config.Load()  // Loads from .env file
server := proxy.NewServer(cfg)
server.Start()
```

**What happens:**
- Loads `GPRXY_USER`, `GPRXY_PASS`, `DB_HOST` from environment
- Creates service user credentials for backend connections
- **Service User Role**: Acts as the "proxy identity" for database connections

### **1.2 Server Startup (`proxy.Start()`)**
```go
// proxy.go:40-56
ln, err := net.Listen("tcp", s.config.Host+":"+s.config.Port)
for {
    conn, err := ln.Accept()  // Accept client connections
    pc := &Connection{conn: conn, config: s.config}
    go pc.handleConnection()  // Each client gets own goroutine
}
```

**Result**: Proxy listens on port 7777, ready for client connections

---

## **Phase 2: Client Connection Establishment**

### **2.1 Client Connection (`psql -h localhost -p 7777 -U testuser2 -d cloudfront_data`)**

```
Client (psql) → Proxy (port 7777)
```

**What happens:**
- `psql` establishes TCP connection to proxy
- Proxy creates `Connection` struct for this client
- Spawns dedicated goroutine: `go pc.handleConnection()`

### **2.2 Connection Handling (`handleConnection()`)**
```go
// proxy.go:62-85
func (pc *Connection) handleConnection() {
    clientAddr := pc.conn.RemoteAddr().String()  // "127.0.0.1:63879"
    pgc := pgproto3.NewBackend(...)              // Protocol handler for client
    
    defer func() {
        pc.conn.Close()           // Close client connection
        if pc.poolConn != nil {
            pc.poolConn.Release() // Return pooled connection
        }
    }()
    
    err := pc.handleStartupMessage(pgc)  // Handle initial messages
    // ... query loop
}
```

---

## **Phase 3: Startup Message Processing**

### **3.1 SSL Request Handling**
```
Client → Proxy: SSLRequest
Proxy → Client: 'N' (SSL rejected)
```

### **3.2 Startup Message Processing (`handleStartupMessage()`)**
```go
// proxy.go:95-155
startupMessage, err := pgconn.ReceiveStartupMessage()

switch msg := startupMessage.(type) {
case *pgproto3.StartupMessage:
    user := msg.Parameters["user"]        // "testuser2"
    database := msg.Parameters["database"] // "cloudfront_data"
    appName := msg.Parameters["application_name"] // "psql"
```

**Extracted Information:**
- **Client User**: `testuser2` (who the client claims to be)
- **Target Database**: `cloudfront_data`
- **Application**: `psql`

---

## **Phase 4: Authentication Process (Proxy-Mediated Authentication)**

> **⚠️ IMPORTANT UPDATE**: The authentication approach has been completely redesigned to support SCRAM-SHA-256. 
> See [scram-authentication-fix.md](./scram-authentication-fix.md) for full details.

### **4.1 New Authentication Architecture**

**Paradigm Shift**: The proxy now **performs authentication itself** rather than transparently relaying messages.

```
┌────────┐                              ┌───────┐                              ┌──────────┐
│ Client │                              │ Proxy │                              │ Backend  │
│        │  AuthenticationCleartext     │       │  SCRAM-SHA-256 / MD5        │          │
│        │ ◄──────────────────────────  │       │  ──────────────────────────► │          │
└────────┘                              └───────┘                              └──────────┘
        Simple cleartext over TLS              Proxy handles complex auth
```

**Key Insight**: 
- Proxy acts as **PostgreSQL server** to client (requests password)
- Proxy acts as **PostgreSQL client** to backend (performs SCRAM/MD5)
- This solves TLS channel binding issues with SCRAM-SHA-256-PLUS

### **4.2 Authentication Initiation**
```go
// internal/proxy/startup.go
err := auth.AuthenticateUser(user, database, pc.config.Host, msg, pgconn, clientAddr)
```

### **4.3 Temporary Connection Creation**
```go
// internal/auth/authenticator.go
backendAddress := host + ":5432"
tempConnection, err := net.DialTimeout("tcp", backendAddress, 10*time.Second)
tempFrontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(tempConnection), tempConnection)
```

**Purpose**: 
- Dedicated connection for authentication only
- Doesn't affect connection pool
- Closed after auth completes

### **4.4 Request Password from Client**
```go
// NEW: Proxy requests password from client
func requestPasswordFromClient(clientBackend *pgproto3.Backend) (string, error) {
    // Send cleartext password request
    clientBackend.Send(&pgproto3.AuthenticationCleartextPassword{})
    
    // Receive password (over TLS)
    msg, _ := clientBackend.Receive()
    return msg.(*pgproto3.PasswordMessage).Password, nil
}
```

**Flow:**
```
Proxy → Client: AuthenticationCleartextPassword {}
Client → Proxy: PasswordMessage { password: "user_password" }
```

**Security**: Password sent over TLS-encrypted connection

### **4.5 Authenticate WITH Backend**
```go
// NEW: Proxy performs authentication as PostgreSQL client
func authenticateWithBackend(frontend *pgproto3.Frontend, username, password string) error {
    for {
        msg, _ := frontend.Receive()
        
        switch msg.(type) {
        case *pgproto3.AuthenticationSASL:
            // Proxy performs SCRAM-SHA-256
            client, _ := scram.SHA256.NewClient(username, password, "")
            conversation := client.NewConversation()
            initialResponse, _ := conversation.Step("")
            
            frontend.Send(&pgproto3.SASLInitialResponse{
                AuthMechanism: "SCRAM-SHA-256",
                Data:          []byte(initialResponse),
            })
            
        case *pgproto3.AuthenticationMD5Password:
            // Proxy computes MD5 hash
            hash := computeMD5(password, username, msg.Salt)
            frontend.Send(&pgproto3.PasswordMessage{Password: hash})
            
        case *pgproto3.AuthenticationOk:
            return nil  // Success!
        }
    }
}
PostgreSQL: Validates and responds "AuthenticationOk"
```

### **4.5 Authentication Completion**
```go
// auth.go:50
log.Printf("[%s] authentication completed successfully", clientAddr)
```

**Result**: Client is verified as legitimate `testuser2` user
**Temporary Connection**: **CLOSED** (disposed after authentication)

---

## **Phase 5: Service User Pool Connection**

### **5.1 Backend Connection Establishment**
```go
// proxy.go:131
err = pc.connectBackend(database, user)
```

**Architecture Shift**: Now we switch from client credentials to **service user credentials**!

### **5.2 Pool Connection Creation (`connectBackend()`)**
```go
// proxy.go:164-177
func (pc *Connection) connectBackend(database, user string) error {
    connectionString := pc.config.BuildConnectionString(database)
    // connectionString = "postgres://testuser:testpass@localhost:5432/cloudfront_data"
    
    connection, err := pool.AcquireConnection(pc.config.ServiceUser, pc.config.ServicePass, database, pc.config.Host)
    pc.poolConn = connection
}
```

**Key Architecture Decision**: 
- **Authentication**: Used client credentials (`testuser2`)
- **Query Execution**: Uses service user credentials (`testuser` from .env)

### **5.3 Pool Manager (`pool.AcquireConnection()`)**
```go
// pool.go:108-128
func AcquireConnection(user, password, database, host string) (*pgxpool.Conn, error) {
    pool, err := GetOrCreatePool(user, password, database, host)
    connection, err := pool.Acquire(ctx)
    return connection, nil
}
```

**Pool Architecture:**
```
poolManager = map[poolKey]*poolInfo{
    {user: "testuser", database: "cloudfront_data"}: &poolInfo{
        pool: pgxpool.Pool (5 connections max),
        created: timestamp,
        lastUsed: timestamp
    }
}
```

**Connection String Used**: `postgres://testuser:testpass@localhost:5432/cloudfront_data`

---

## **Phase 6: Query Execution Setup**

### **6.1 Protocol Bridge Setup**
```go
// proxy.go:145-150
underlyingConn := pc.poolConn.Conn().PgConn().Conn()
bf := pgproto3.NewFrontend(pgproto3.NewChunkReader(underlyingConn), underlyingConn)
pc.bf = bf
pc.user = user  // "testuser2" (for logging)
pc.db = database
```

**Final Architecture:**
```
Client ←→ Proxy ←→ PostgreSQL Pool Connection (Service User)
```

### **6.2 Query Loop Entry**
```go
// proxy.go:152
log.Printf("[%s] entering query handling loop", clientAddr)
for {
    err := pc.handleMessage(pgconn)  // Handle each query
}
```

---

## **Phase 7: Query Processing**

### **7.1 Query Reception (`handleMessage()`)**
```go
// proxy.go:179-220
msg, err := client.Receive()  // Get query from client

switch query := msg.(type) {
case *pgproto3.Query:
    log.Printf("[%s] [%s] QUERY: %s", clientAddr, pc.user, query.String)
case *pgproto3.Parse:
    log.Printf("[%s] [%s] PARSE: statement='%s' query='%s'", ...)
}
```

### **7.2 Query Forwarding**
```go
// proxy.go:245
err = pc.bf.Send(msg)  // Forward to pooled connection
```

**What happens:**
- Client sends: `SELECT * FROM users;`
- Proxy forwards to PostgreSQL via **service user connection**
- PostgreSQL executes as **service user** (not client user!)

### **7.3 Response Relay (`relayBackendResponse()`)**
```go
// proxy.go:254-285
for {
    msg, err := pc.bf.Receive()  // Get response from PostgreSQL
    err = client.Send(msg)       // Forward to client
    
    switch msgType := msg.(type) {
    case *pgproto3.ReadyForQuery:
        return nil  // Query complete
    case *pgproto3.ErrorResponse:
        log.Printf("[%s] query error: %s", clientAddr, msgType.Message)
    }
}
```

---

# 🔄 **Complete Data Flow Summary**

## **Authentication Phase:**
```
1. Client connects to Proxy:7777
2. Proxy creates temporary connection to PostgreSQL:5432
3. Client credentials validated through temporary connection
4. Temporary connection closed
```

## **Query Execution Phase:**
```
1. Proxy acquires pooled connection using SERVICE USER credentials
2. Client queries forwarded through service user connection
3. All database operations execute as SERVICE USER
4. Results returned to client
```

---

# 🏗️ **Architecture Components**

## **1. Connection Types**
- **Client Connection**: `psql` ↔ Proxy (port 7777)
- **Temporary Connection**: Proxy ↔ PostgreSQL (authentication only)
- **Pooled Connection**: Proxy ↔ PostgreSQL (query execution, service user)

## **2. User Identities**
- **Client User**: `testuser2` (authentication identity)
- **Service User**: `testuser` (execution identity)
- **Database sees**: All queries from `testuser` (service user)

## **3. Pool Management**
- **Key**: `{user: "testuser", database: "cloudfront_data"}`
- **Pool Size**: 5 connections max per database
- **Reuse**: Multiple clients share same service user pool

---

# 🚨 **Current Architecture Limitations**

## **1. Identity Loss**
- Client authenticates as `testuser2`
- Queries execute as `testuser` (service user)
- **Database audit logs show service user, not client user**

## **2. Permission Issues**
- Service user needs permissions for ALL client operations
- No per-user permission enforcement
- Security boundary is at proxy level, not database level

## **3. Connection Efficiency**
- Authentication creates temporary connections (overhead)
- Pool connections are service-user only (not per-client-user)

---

# 💡 **Why This Architecture Exists**

## **Benefits:**
1. **Connection Pooling**: Efficient resource usage
2. **Centralized Authentication**: Proxy validates all users
3. **Simplified Database Setup**: Only service user needed in DB
4. **Protocol Transparency**: Clients see normal PostgreSQL behavior

## **Trade-offs:**
1. **Lost User Identity**: Database doesn't see real users
2. **Complex Authentication**: Dual-connection overhead
3. **Service User Permissions**: Needs broad database access

This architecture is common in **connection pooling proxies** where the focus is on **performance and resource management** rather than **user identity preservation**. Your recent improvements with connection state checking help solve the client disconnection issues during the authentication handoff phase.



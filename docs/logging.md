# Logging System

The proxy uses a centralized logging system with two logging levels: **DEBUG** and **INFO** (production).

## Log Levels

### INFO (Production Mode)
- **Purpose**: Production logging with essential operational information
- **What's logged**:
  - Connection requests (user, database, application)
  - Authentication success/failures
  - Query execution
  - Client connections and disconnections
  - Cancel requests
  - Errors and warnings
  - TLS configuration
  - Pool creation

### DEBUG (Development Mode)
- **Purpose**: Detailed debugging information for development and troubleshooting
- **What's logged**: Everything in INFO plus:
  - Startup message types
  - Backend connection establishment timing
  - Pool connection details (PID, secret keys)
  - BackendKeyData exchanges
  - ReadyForQuery messages
  - SSL/TLS negotiation steps
  - Authentication protocol details (SCRAM, MD5, etc.)
  - Parameter status messages
  - Query protocol details (Parse, Bind, Execute, Sync, Describe)
  - Command completion details
  - Pool statistics
  - Connection registry operations

## Usage

### Setting the Log Level

The logging level is controlled via the `LOG_LEVEL` environment variable:

#### Production Mode (Default)
```bash
# No LOG_LEVEL set or LOG_LEVEL not set to "debug"
./gprxy

# Or explicitly set to production
LOG_LEVEL=production ./gprxy
```

#### Debug Mode
```bash
LOG_LEVEL=debug ./gprxy
```

### Using with .env file

Add to your `.env` file:
```bash
# For production
LOG_LEVEL=production

# For debug
LOG_LEVEL=debug
```

## Log Format

All logs follow a consistent format:

```
[LEVEL] [client_address] message
```

Examples:
```
[INFO] [127.0.0.1:63273] connection request - user: testuser_scram, database: postgres, app: psql
[INFO] [127.0.0.1:63273] user testuser_scram authenticated successfully
[INFO] [127.0.0.1:63273] [testuser_scram] query: SELECT * FROM users;
[DEBUG] [127.0.0.1:63273] backend connection established in 28.674458ms
[DEBUG] [127.0.0.1:63273] pool connection backend key: PID=85383, secret_key=560336618
[ERROR] [127.0.0.1:63273] failed to connect to backend: connection refused
[WARN] [127.0.0.1:63284] cancel request for unknown connection
```

## Log Level Guidelines

### When to Use INFO (Production)
- Running in production environments
- When you only need to see:
  - User activity and queries
  - Connection lifecycle
  - Errors and warnings
  - System startup/configuration

### When to Use DEBUG
- Development and testing
- Troubleshooting authentication issues
- Debugging connection pooling
- Investigating protocol-level issues
- Understanding the internal flow
- Diagnosing cancel request problems

## Implementation Details

The logging system is implemented in `/internal/logger/logger.go` and provides:

- `logger.Info(format, args...)` - Production level logs
- `logger.Debug(format, args...)` - Debug level logs
- `logger.Error(format, args...)` - Error logs (always shown)
- `logger.Warn(format, args...)` - Warning logs (always shown)

The logger is initialized at application startup in `main.go` using `logger.InitFromEnv()`.

## Example: Comparing Log Output

### Production Mode Output
```
[INFO] PostgreSQL proxy listening on 127.0.0.1:7777 (TLS: enabled)
[INFO] [127.0.0.1:63273] connection request - user: testuser_scram, database: postgres, app: psql
[INFO] [127.0.0.1:63273] user testuser_scram authenticated successfully
[INFO] [127.0.0.1:63273] [testuser_scram] query: SELECT pg_sleep(60);
[INFO] [127.0.0.1:63284] cancel request received: PID=85383, secret_key=560336618
[INFO] [127.0.0.1:63284] cancel request forwarded successfully
[WARN] [127.0.0.1:63273] query error: canceling statement due to user request (code: 57014)
[INFO] [127.0.0.1:63273] connection closed
```

### Debug Mode Output
```
[INFO] Debug logging enabled
[INFO] TLS configured successfully (cert: certs/postgres.crt, key: certs/postgres.key)
[INFO] PostgreSQL proxy listening on 127.0.0.1:7777 (TLS: enabled)
[DEBUG] [127.0.0.1:63273] new client connection established
[DEBUG] [127.0.0.1:63273] received startup message type: *pgproto3.SSLRequest
[DEBUG] [127.0.0.1:63273] SSL request received
[DEBUG] [127.0.0.1:63273] SSL configured, upgrading connection to TLS
[DEBUG] [127.0.0.1:63273] TLS handshake completed successfully
[DEBUG] [127.0.0.1:63273] received startup message type: *pgproto3.StartupMessage
[INFO] [127.0.0.1:63273] connection request - user: testuser_scram, database: postgres, app: psql
[DEBUG] [127.0.0.1:63273] connecting to PostgreSQL at localhost:5432 as testuser_scram for authentication
[DEBUG] [127.0.0.1:63273] sending startup message to PostgreSQL
[DEBUG] [127.0.0.1:63273] backend auth message: *pgproto3.AuthenticationSASL
[DEBUG] [127.0.0.1:63273] backend requests SASL auth, mechanisms: [SCRAM-SHA-256]
[DEBUG] [127.0.0.1:63273] sending SCRAM initial response to backend
[DEBUG] [127.0.0.1:63273] backend auth message: *pgproto3.AuthenticationSASLContinue
[DEBUG] [127.0.0.1:63273] backend SASL continue
[DEBUG] [127.0.0.1:63273] backend auth message: *pgproto3.AuthenticationSASLFinal
[DEBUG] [127.0.0.1:63273] backend SASL final
[DEBUG] [127.0.0.1:63273] backend auth message: *pgproto3.AuthenticationOk
[DEBUG] [127.0.0.1:63273] backend authentication OK
[DEBUG] [127.0.0.1:63273] backend auth message: *pgproto3.ParameterStatus
[DEBUG] [127.0.0.1:63273] parameter status: application_name = psql
[DEBUG] [127.0.0.1:63273] backend auth message: *pgproto3.BackendKeyData
[DEBUG] [127.0.0.1:63273] temp auth connection backend key data received (will not forward): PID=85382, secret_key=3593045559
[DEBUG] [127.0.0.1:63273] backend auth message: *pgproto3.ReadyForQuery
[DEBUG] [127.0.0.1:63273] temp auth connection ready for queries (will not forward ReadyForQuery yet)
[DEBUG] [127.0.0.1:63273] authentication completed successfully
[INFO] [127.0.0.1:63273] user testuser_scram authenticated successfully
[INFO] created connection pool for database: postgres
[DEBUG] [127.0.0.1:63273] acquired connection from pool for database: postgres
[DEBUG] pool stats for [testuser_scram,postgres] - total: 1, acquired: 1, idle: 0
[DEBUG] [127.0.0.1:63273] backend connection established in 28.674458ms
[DEBUG] [127.0.0.1:63273] pool connection backend key: PID=85383, secret_key=560336618
[DEBUG] [127.0.0.1:63273] sent pool BackendKeyData to client
[DEBUG] [127.0.0.1:63273] sent ReadyForQuery to client
[DEBUG] registered connection: PID=85383, secret_key=85383, map_key=366717752970986
[DEBUG] active connections in registry: 1
[DEBUG] [127.0.0.1:63273] registered connection: PID=85383, secret_key=560336618
[DEBUG] [127.0.0.1:63273] entering query handling loop
[INFO] [127.0.0.1:63273] [testuser_scram] query: SELECT pg_sleep(60);
[DEBUG] [127.0.0.1:63273] query connection PID=85383, secret_key=560336618
[INFO] [127.0.0.1:63284] cancel request received: PID=85383, secret_key=560336618
[DEBUG] looking for cancel key: PID=85383, secret_key=560336618, computed_key=366717752970986
[DEBUG] active connections in registry: 1
[DEBUG] registry entry: key=366717752970986, user=testuser_scram, db=postgres
[DEBUG] found connection for cancel request: user=testuser_scram, db=postgres
[DEBUG] [127.0.0.1:63284] found target connection: user=testuser_scram, db=postgres
[DEBUG] cancel request forwarded to backend: PID=85383, secret_key=560336618
[INFO] [127.0.0.1:63284] cancel request forwarded successfully
[WARN] [127.0.0.1:63273] query error: canceling statement due to user request (code: 57014)
[DEBUG] [127.0.0.1:63273] query completed, ready for next query (status: I)
[DEBUG] [127.0.0.1:63273] released connection back to pool
[INFO] [127.0.0.1:63273] connection closed
```

As you can see, debug mode provides significantly more detail about the internal workings of the proxy, while production mode focuses on actionable information.


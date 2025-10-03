
# TLS/SSL Implementation in gprxy - Complete Guide

## Table of Contents
1. [Overview](#overview)
2. [Architecture](#architecture)
3. [Implementation Flow](#implementation-flow)
4. [Certificate Setup](#certificate-setup)
5. [Configuration](#configuration)
6. [Security Best Practices](#security-best-practices)
7. [Testing](#testing)
8. [Troubleshooting](#troubleshooting)
9. [Performance Considerations](#performance-considerations)

## Overview

gprxy implements **PostgreSQL-native SSL/TLS encryption** for client-to-proxy connections, following the official PostgreSQL wire protocol for SSL negotiation.

### Key Features

✅ **PostgreSQL Protocol Compliance**: Implements standard SSL negotiation
✅ **TLS 1.2/1.3 Support**: Modern encryption standards
✅ **Optional TLS**: Works with and without TLS configuration
✅ **Zero Downtime**: TLS upgrade happens on-demand per connection
✅ **Proper Error Handling**: Clear error messages for TLS failures
✅ **Production Ready**: Tested with modern cipher suites

### Encryption Scope

```
┌─────────┐    TLS ✅     ┌───────┐    Plain TCP    ┌────────────┐
│ Client  │ ────────────> │ Proxy │ ──────────────> │ PostgreSQL │
└─────────┘   Encrypted   └───────┘   (Backend)     └────────────┘
```

**Current Implementation:**
- ✅ Client → Proxy: **TLS Encrypted** (this guide)
- ⚠️ Proxy → PostgreSQL: **Plain TCP** (future enhancement)

---

## Architecture

### SSL Negotiation Flow (PostgreSQL Protocol)

The implementation follows PostgreSQL's standard SSL negotiation:

```
1. Client Connection
   ├─ TCP connection established
   └─ Plain TCP (no encryption yet)

2. SSL Negotiation
   ├─ Client: SSLRequest message
   ├─ Proxy: 'S' (accept) or 'N' (reject)
   └─ If 'S': TLS handshake begins

3. TLS Handshake
   ├─ Client: ClientHello
   ├─ Proxy: ServerHello, Certificate
   ├─ Client: Validate certificate
   └─ Both: Establish encryption keys

4. Encrypted Connection
   ├─ Client: StartupMessage (over TLS)
   ├─ Authentication (over TLS)
   └─ All queries (over TLS)
```

### Code Architecture

```go
// Server Structure
type Server struct {
    config    *config.Config
    tlsConfig *tls.Config  // ← TLS configuration
}

// Connection Structure
type Connection struct {
    conn      net.Conn      // ← May be plain TCP or *tls.Conn
    tlsConfig *tls.Config   // ← TLS config passed from server
    // ... other fields
}
```

### Key Components

| Component | File | Purpose |
|-----------|------|---------|
| **TLS Loader** | `internal/tls/tls.go` | Loads certificates, creates TLS config |
| **Server** | `internal/proxy/proxy.go` | Handles SSL negotiation |
| **Connection Handler** | `internal/proxy/proxy.go` | Manages TLS upgrade |

---

## Implementation Flow

### Detailed Connection Flow with TLS

```
┌─────────────────────────────────────────────────────────────┐
│                    CONNECTION LIFECYCLE                       │
└─────────────────────────────────────────────────────────────┘

Step 1: Server Startup
┌──────────────────────────────────────┐
│ main.go                              │
│  ├─ tls.Load()                       │
│  │   └─ Loads PROXY_CERT, PROXY_KEY │
│  ├─ config.Load()                    │
│  └─ proxy.NewServer(cfg, tls)       │
└──────────────────────────────────────┘

Step 2: Client Connects (Plain TCP)
┌──────────────────────────────────────┐
│ proxy.Start()                        │
│  ├─ net.Listen("tcp", ":7777")      │
│  ├─ conn, _ := ln.Accept()          │
│  └─ go pc.handleConnection()        │
└──────────────────────────────────────┘

Step 3: SSL Negotiation
┌──────────────────────────────────────┐
│ handleConnection()                   │
│  ├─ pgc := pgproto3.NewBackend(conn)│
│  └─ handleStartupMessage(pgc)       │
│                                      │
│ handleStartupMessage()               │
│  ├─ msg := ReceiveStartupMessage()  │
│  └─ switch msg.(type):              │
│      case *pgproto3.SSLRequest:     │
│        ├─ Check if tlsConfig != nil │
│        ├─ Write 'S' to client       │
│        └─ Upgrade to TLS            │
└──────────────────────────────────────┘

Step 4: TLS Upgrade
┌──────────────────────────────────────┐
│ TLS Handshake                        │
│  ├─ tlsConn := tls.Server(conn, cfg)│
│  ├─ tlsConn.Handshake()             │
│  ├─ pc.conn = tlsConn               │
│  └─ pgconn = NewBackend(tlsConn)    │
└──────────────────────────────────────┘

Step 5: Encrypted Communication
┌──────────────────────────────────────┐
│ Over TLS Connection                  │
│  ├─ StartupMessage                   │
│  ├─ Authentication                   │
│  ├─ Queries                          │
│  └─ Results                          │
└──────────────────────────────────────┘
```

### Code Walkthrough

#### 1. TLS Configuration Loading (`internal/tls/tls.go`)

```go
func Load() *tls.Config {
    proxyCert := os.Getenv("PROXY_CERT")  // Path to certificate
    proxyKey := os.Getenv("PROXY_KEY")    // Path to private key
    
    // Optional: Return nil if not configured
    if proxyCert == "" || proxyKey == "" {
        return nil  // Proxy runs without TLS
    }
    
    // Load certificate
    cert, err := tls.LoadX509KeyPair(proxyCert, proxyKey)
    
    // Create secure TLS config
    config := &tls.Config{
        Certificates: []tls.Certificate{cert},
        MinVersion:   tls.VersionTLS12,           // TLS 1.2 minimum
        MaxVersion:   tls.VersionTLS13,           // TLS 1.3 maximum
        
        // Security hardening
        PreferServerCipherSuites: true,
        CipherSuites: []uint16{
            tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
            tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
            // ... more secure ciphers
        },
    }
    
    return config
}
```

**Key Points:**
- Returns `nil` if TLS not configured (graceful degradation)
- Uses modern cipher suites only
- Enforces TLS 1.2+ (no SSL 3.0, TLS 1.0, TLS 1.1)

#### 2. Server Initialization (`internal/proxy/proxy.go`)

```go
func (s *Server) Start() error {
    // Plain TCP listener (not TLS listener!)
    ln, err := net.Listen("tcp", s.config.Host+":"+s.config.Port)
    
    // Log TLS status
    tlsStatus := "disabled"
    if s.tlsConfig != nil {
        tlsStatus = "enabled"
    }
    log.Printf("PostgreSQL proxy listening on %s (TLS: %s)", ln.Addr(), tlsStatus)
    
    // Accept connections
    for {
        conn, err := ln.Accept()  // Plain TCP connection
        
        pc := &Connection{
            conn:      conn,
            tlsConfig: s.tlsConfig,  // Pass TLS config to connection
        }
        go pc.handleConnection()
    }
}
```

**Why Plain TCP Listener?**
- PostgreSQL protocol requires SSL negotiation **after** connection
- Can't use `tls.Listen()` directly (would force TLS immediately)
- TLS upgrade happens **during** protocol negotiation

#### 3. SSL Negotiation (`handleStartupMessage`)

```go
func (pc *Connection) handleStartupMessage(pgconn *pgproto3.Backend) (*pgproto3.Backend, error) {
    msg, _ := pgconn.ReceiveStartupMessage()
    
    switch msg := msg.(type) {
    case *pgproto3.SSLRequest:
        // Check if TLS is configured
        if pc.tlsConfig == nil {
            // TLS not available
            pc.conn.Write([]byte{'N'})  // Reject SSL
            return pc.handleStartupMessage(pgconn)  // Continue without TLS
        }
        
        // TLS is configured - accept SSL
        pc.conn.Write([]byte{'S'})  // Accept SSL
        
        // Upgrade to TLS
        tlsConn := tls.Server(pc.conn, pc.tlsConfig)
        err := tlsConn.Handshake()  // Perform TLS handshake
        if err != nil {
            return nil, fmt.Errorf("TLS handshake failed: %w", err)
        }
        
        // Replace connection with TLS connection
        pc.conn = tlsConn
        
        // Create new pgproto3.Backend with TLS connection
        newPgconn := pgproto3.NewBackend(pgproto3.NewChunkReader(tlsConn), tlsConn)
        
        // Now receive StartupMessage over encrypted connection
        return pc.handleStartupMessage(newPgconn)
    
    case *pgproto3.StartupMessage:
        // Regular authentication flow
        // ... authentication code ...
        return pgconn, nil
    }
}
```

**Critical Design Decisions:**

1. **Return New pgconn**: After TLS upgrade, return new `pgproto3.Backend` that wraps TLS connection
2. **Recursive Call**: Call `handleStartupMessage` again to receive actual `StartupMessage` over TLS
3. **Connection Replacement**: Update `pc.conn` so all future I/O uses TLS

#### 4. Query Handling (Over TLS)

```go
func (pc *Connection) handleConnection() {
    // Create initial pgconn with plain TCP
    pgc := pgproto3.NewBackend(pgproto3.NewChunkReader(pc.conn), pc.conn)
    
    // May return different pgconn if TLS upgrade happens
    pgc, err := pc.handleStartupMessage(pgc)  // ← Receives TLS pgconn if upgraded
    
    // Query loop uses the correct pgconn (TLS or plain)
    for {
        pc.handleMessage(pgc)  // All queries over TLS if upgraded
    }
}
```

---

## Certificate Setup

### Quick Start: Self-Signed Certificates

For development and testing:

```bash
# 1. Generate private key
openssl genrsa -out certs/postgres.key 2048

# 2. Generate certificate signing request
openssl req -new -key certs/postgres.key -out certs/postgres.csr \
    -config certs/server.cnf

# 3. Generate self-signed certificate (valid for 365 days)
openssl x509 -req -in certs/postgres.csr \
    -signkey certs/postgres.key \
    -out certs/postgres.crt \
    -days 365 \
    -extensions v3_ext \
    -extfile certs/server.cnf

# 4. Set permissions
chmod 600 certs/postgres.key
chmod 644 certs/postgres.crt
```

### Certificate Configuration File (`certs/server.cnf`)

```ini
[req]
default_md = sha256
prompt = no
req_extensions = v3_ext
distinguished_name = req_distinguished_name

[req_distinguished_name]
CN = localhost

[v3_ext]
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = critical,serverAuth,clientAuth
subjectAltName = DNS:localhost,DNS:proxy.example.com,IP:127.0.0.1
```

**Important Fields:**
- `CN`: Common Name (hostname)
- `subjectAltName`: Alternative names (required for modern clients)
- `keyUsage`: Certificate purpose
- `extendedKeyUsage`: TLS server authentication

### Production: CA-Signed Certificates

For production, use certificates from a trusted CA:

```bash
# Option 1: Let's Encrypt (Free, automated)
certbot certonly --standalone -d proxy.yourdomain.com

# Option 2: Purchase from CA (DigiCert, GlobalSign, etc.)
# Follow CA's certificate generation process

# Option 3: Internal CA (for private networks)
# Use your organization's certificate authority
```

### Certificate Requirements

| Requirement | Value | Notes |
|-------------|-------|-------|
| **Algorithm** | RSA 2048+ or ECDSA P-256+ | Modern encryption |
| **Hash** | SHA-256 | No MD5 or SHA-1 |
| **Validity** | ≤ 398 days | Browser/client requirements |
| **Subject Alt Names** | Required | CN alone is deprecated |
| **Key Usage** | digitalSignature, keyEncipherment | TLS server |
| **Extended Key Usage** | serverAuth | TLS authentication |

---

## Configuration

### Environment Variables

```bash
# .env file
# TLS Configuration (optional - proxy works without these)
PROXY_CERT=certs/postgres.crt
PROXY_KEY=certs/postgres.key

# Database Configuration
DB_HOST=localhost
GPRXY_USER=your_service_user
GPRXY_PASS=your_service_password
```

### Client Connection Strings

#### With TLS (Recommended)

```bash
# Require TLS (fail if TLS not available)
psql "postgresql://user@localhost:7777/db?sslmode=require"

# Verify certificate (production)
psql "postgresql://user@localhost:7777/db?sslmode=verify-full&sslrootcert=/path/to/ca.crt"

# Verify CA only (no hostname check)
psql "postgresql://user@localhost:7777/db?sslmode=verify-ca&sslrootcert=/path/to/ca.crt"
```

#### Without TLS

```bash
# Disable TLS (plain TCP)
psql "postgresql://user@localhost:7777/db?sslmode=disable"

# Prefer TLS but allow plain (default)
psql "postgresql://user@localhost:7777/db?sslmode=prefer"
```

### SSL Modes Explained

| Mode | Behavior | Use Case |
|------|----------|----------|
| `disable` | No TLS, plain TCP only | Local development, testing |
| `allow` | Try plain, fall back to TLS | Legacy compatibility |
| `prefer` | Try TLS, fall back to plain | Default, flexible |
| `require` | TLS required, no cert verification | Production (self-signed certs) |
| `verify-ca` | TLS + verify CA | Production (trusted CA) |
| `verify-full` | TLS + verify CA + hostname | Production (recommended) |

---

## Security Best Practices

### 1. Certificate Management

✅ **DO:**
- Use strong private keys (RSA 2048+ or ECDSA P-256+)
- Protect private keys with file permissions (`chmod 600`)
- Rotate certificates before expiration
- Use certificates from trusted CAs in production
- Include proper Subject Alternative Names

❌ **DON'T:**
- Commit private keys to version control
- Use self-signed certs in production
- Use weak algorithms (RSA 1024, MD5, SHA-1)
- Share certificates across environments
- Use wildcards in public-facing proxies

### 2. TLS Configuration

✅ **Current Implementation (Good):**
```go
config := &tls.Config{
    MinVersion:   tls.VersionTLS12,  // TLS 1.2+
    MaxVersion:   tls.VersionTLS13,  // Allow TLS 1.3
    PreferServerCipherSuites: true,   // Server chooses cipher
    CipherSuites: []uint16{
        // Only secure, modern ciphers
        tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
        tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
    },
}
```

### 3. Security Enhancements (Future)

#### Mutual TLS (mTLS) - Client Certificates

```go
config := &tls.Config{
    ClientAuth: tls.RequireAndVerifyClientCert,
    ClientCAs:  clientCAPool,  // Trusted client CAs
}
```

#### Certificate Pinning

```go
config := &tls.Config{
    VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
        // Custom certificate validation
        return validateCertFingerprint(rawCerts[0])
    },
}
```

### 4. Monitoring & Logging

**What to Log:**
```go
log.Printf("[%s] TLS version: %s", addr, tlsConn.ConnectionState().Version)
log.Printf("[%s] Cipher suite: %s", addr, tls.CipherSuiteName(tlsConn.ConnectionState().CipherSuite))
log.Printf("[%s] Client certificate: %v", addr, tlsConn.ConnectionState().PeerCertificates)
```

**Metrics to Track:**
- TLS connection success/failure rate
- TLS handshake duration
- TLS version distribution
- Cipher suite usage
- Certificate expiration dates

---

## Testing

### Manual Testing

#### Test 1: TLS Connection
```bash
# Connect with TLS
psql "postgresql://testuser@localhost:7777/postgres?sslmode=require"

# Expected log output:
# [127.0.0.1:xxxxx] SSL request received
# [127.0.0.1:xxxxx] SSL configured, upgrading connection to TLS
# [127.0.0.1:xxxxx] TLS handshake completed successfully
```

#### Test 2: Non-TLS Connection
```bash
# Connect without TLS
psql "postgresql://testuser@localhost:7777/postgres?sslmode=disable"

# Expected log output:
# [127.0.0.1:xxxxx] received startup message type: *pgproto3.StartupMessage
# (No SSL request)
```

#### Test 3: Certificate Verification
```bash
# Verify certificate
psql "postgresql://testuser@localhost:7777/postgres?sslmode=verify-full&sslrootcert=certs/ca.crt"

# Should fail with self-signed cert if hostname doesn't match
```

### OpenSSL Testing

```bash
# Test TLS handshake
openssl s_client -connect localhost:7777 -starttls postgres

# Expected output:
# CONNECTED(00000003)
# depth=0 CN = localhost
# verify error:num=18:self signed certificate
# Certificate chain
#  0 s:CN = localhost
#    i:CN = localhost
# ---
# SSL handshake has read 1234 bytes
# ...
```

### Automated Testing Script

```bash
#!/bin/bash
# test-tls.sh

echo "Testing TLS Implementation"
echo "=========================="

# Test 1: TLS connection
echo "Test 1: TLS Connection (sslmode=require)"
PGPASSWORD=testpass psql "postgresql://testuser@localhost:7777/postgres?sslmode=require" -c "SELECT 'TLS works!' AS result" && echo "✓ PASS" || echo "✗ FAIL"

# Test 2: Non-TLS connection
echo "Test 2: Non-TLS Connection (sslmode=disable)"
PGPASSWORD=testpass psql "postgresql://testuser@localhost:7777/postgres?sslmode=disable" -c "SELECT 'Plain TCP works!' AS result" && echo "✓ PASS" || echo "✗ FAIL"

# Test 3: TLS preference
echo "Test 3: TLS Preference (sslmode=prefer)"
PGPASSWORD=testpass psql "postgresql://testuser@localhost:7777/postgres?sslmode=prefer" -c "SELECT version()" && echo "✓ PASS" || echo "✗ FAIL"

echo "=========================="
echo "Testing complete!"
```

---

## Troubleshooting

### Common Issues

#### Issue 1: "TLS handshake failed"

**Symptoms:**
```
[127.0.0.1:xxxxx] TLS handshake failed: tls: first record does not look like a TLS handshake
```

**Causes:**
- Client sending non-TLS data to TLS-expecting proxy
- Protocol mismatch
- Client using `sslmode=disable` but proxy forcing TLS

**Solution:**
- Ensure client uses `sslmode=require` or `sslmode=prefer`
- Check if proxy TLS config is correct
- Verify certificate files exist and are readable

#### Issue 2: Certificate Verification Failed

**Symptoms:**
```
psql: error: SSL error: certificate verify failed
```

**Causes:**
- Self-signed certificate without proper CA
- Hostname mismatch in certificate
- Missing Subject Alternative Names
- Client can't find CA certificate

**Solutions:**
```bash
# For self-signed certs, use sslmode=require (no verification)
psql "postgresql://user@localhost:7777/db?sslmode=require"

# For CA verification, specify CA file
psql "postgresql://user@localhost:7777/db?sslmode=verify-ca&sslrootcert=certs/ca.crt"

# For full verification, ensure hostname matches certificate CN/SAN
psql "postgresql://user@proxy.example.com:7777/db?sslmode=verify-full&sslrootcert=certs/ca.crt"
```

#### Issue 3: Permission Denied on Certificate Files

**Symptoms:**
```
Failed to load TLS certificate: open certs/postgres.key: permission denied
```

**Solution:**
```bash
# Fix permissions
chmod 600 certs/postgres.key
chmod 644 certs/postgres.crt

# Verify
ls -l certs/
# Expected:
# -rw------- 1 user group ... postgres.key
# -rw-r--r-- 1 user group ... postgres.crt
```

#### Issue 4: "unknown message type" After TLS Upgrade

**Symptoms:**
```
[127.0.0.1:xxxxx] query handling error: client receive error: unknown message type
```

**Cause:**
- Query handler using wrong pgconn (not updated after TLS upgrade)

**Solution:**
- Ensure `handleStartupMessage` returns new pgconn after TLS upgrade
- Use returned pgconn in query loop

```go
// ✓ CORRECT
pgc, err := pc.handleStartupMessage(pgc)  // Capture returned pgconn
for {
    pc.handleMessage(pgc)  // Use updated pgconn
}

// ✗ WRONG
pc.handleStartupMessage(pgc)  // Ignoring returned pgconn
for {
    pc.handleMessage(pgc)  // Using old pgconn!
}
```

### Debug Mode

Enable detailed TLS logging:

```go
// In internal/tls/tls.go
import "crypto/tls"

func Load() *tls.Config {
    config := &tls.Config{
        // ... existing config ...
        
        // Enable debug logging
        KeyLogWriter: os.Stderr,  // For Wireshark decryption
    }
    
    return config
}
```

### Wireshark Analysis

Capture TLS traffic for analysis:

```bash
# 1. Set SSLKEYLOGFILE environment variable
export SSLKEYLOGFILE=/tmp/sslkeys.log

# 2. Run proxy
go run main.go

# 3. Capture traffic
tcpdump -i lo0 -w /tmp/proxy.pcap port 7777

# 4. Open in Wireshark, configure SSL key log file
# Edit → Preferences → Protocols → TLS → (Pre)-Master-Secret log filename
```

---

## Performance Considerations

### TLS Overhead

**Typical Impact:**
- Initial handshake: ~1-5ms (one-time per connection)
- Encryption/decryption: ~5-10% CPU overhead
- Memory: ~40KB per TLS connection

**Mitigations:**
- Use connection pooling (already implemented!)
- Enable TLS session resumption
- Use hardware acceleration if available

### TLS Session Resumption

Enable session resumption for faster reconnections:

```go
config := &tls.Config{
    // ... existing config ...
    
    // Session cache for resumption
    ClientSessionCache: tls.NewLRUClientSessionCache(100),
    
    // Session tickets
    SessionTicketsDisabled: false,
}
```

### Benchmarking

```bash
# Benchmark without TLS
pgbench -h localhost -p 7777 -U testuser -d postgres -c 10 -j 2 -T 60 --no-vacuum -C

# Benchmark with TLS
PGSSLMODE=require pgbench -h localhost -p 7777 -U testuser -d postgres -c 10 -j 2 -T 60 --no-vacuum -C

# Compare results
```

**Expected Results:**
- TLS adds 2-10% latency depending on workload
- Connection establishment: +1-5ms per connection
- Query throughput: -5-10% with TLS

---

## Future Enhancements

### 1. Proxy → PostgreSQL TLS

Encrypt backend connections:

```go
connectionString := fmt.Sprintf(
    "postgres://%s:%s@%s:5432/%s?sslmode=require",
    serviceUser, servicePass, host, database,
)
```

### 2. Certificate Rotation

Hot reload certificates without restart:

```go
type DynamicTLSConfig struct {
    mu     sync.RWMutex
    config *tls.Config
}

func (d *DynamicTLSConfig) GetConfigForClient(*tls.ClientHelloInfo) (*tls.Config, error) {
    d.mu.RLock()
    defer d.mu.RUnlock()
    return d.config, nil
}
```

### 3. Mutual TLS (mTLS)

Require client certificates:

```go
config := &tls.Config{
    ClientAuth: tls.RequireAndVerifyClientCert,
    ClientCAs:  loadClientCAPool(),
}
```

### 4. ALPN (Application-Layer Protocol Negotiation)

Support protocol negotiation:

```go
config := &tls.Config{
    NextProtos: []string{"postgresql"},
}
```

### 5. TLS Metrics

Track TLS usage:

```go
type TLSMetrics struct {
    TotalConnections    int64
    TLSConnections      int64
    PlainConnections    int64
    HandshakeFailures   int64
    AverageHandshakeTime time.Duration
}
```

---

## Summary

### What We've Implemented ✅

1. **PostgreSQL-compliant SSL negotiation**
2. **TLS 1.2/1.3 support with modern ciphers**
3. **Graceful TLS/non-TLS operation**
4. **Proper connection upgrade handling**
5. **Security best practices**

### Production Checklist

Before deploying to production:

- [ ] Use CA-signed certificates (not self-signed)
- [ ] Configure proper Subject Alternative Names
- [ ] Set restrictive file permissions on private keys
- [ ] Enable TLS 1.3 only (remove TLS 1.2 support)
- [ ] Implement certificate rotation
- [ ] Add TLS monitoring and alerting
- [ ] Test certificate expiration scenarios
- [ ] Document certificate renewal process
- [ ] Consider mutual TLS for high-security environments
- [ ] Encrypt proxy → PostgreSQL connections

### Quick Reference

```bash
# Generate certificates
openssl genrsa -out certs/postgres.key 2048
openssl req -new -key certs/postgres.key -out certs/postgres.csr -config certs/server.cnf
openssl x509 -req -in certs/postgres.csr -signkey certs/postgres.key -out certs/postgres.crt -days 365

# Configure
export PROXY_CERT=certs/postgres.crt
export PROXY_KEY=certs/postgres.key

# Test
psql "postgresql://user@localhost:7777/db?sslmode=require"
```

---


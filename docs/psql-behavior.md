# psql Double Connection Behavior Explained

## The Issue

When connecting via `psql` without a cached password, you'll see TWO connection attempts:

1. **First connection**: Client disconnects with "unexpected EOF" after receiving auth challenge
2. **Second connection**: Full authentication succeeds

## Why This Happens

This is **standard psql behavior**, not a proxy bug.

### Interactive Password Flow

When `psql` connects interactively without a cached password:

```
Step 1: Probe Connection
├─ Client → StartupMessage
├─ Server → AuthenticationMD5Password (or SCRAM)
└─ Client → Disconnect (to prompt user)

Step 2: Interactive Prompt
└─ Terminal: "Password for user testuser:" [user types password]

Step 3: Real Connection
├─ Client → StartupMessage
├─ Server → AuthenticationMD5Password
├─ Client → PasswordMessage (password ready)
└─ Server → AuthenticationOK
```

### Why psql Does This

- Different auth methods (MD5, SCRAM, Kerberos, certs) require different user interactions
- `psql` must probe the server to determine which authentication method is needed
- Only then can it prompt the user appropriately
- It makes a NEW connection because the original connection times out during user interaction

## How to Avoid Double Connections

### Option 1: Use .pgpass file (Recommended)

```bash
echo "localhost:7777:postgres:testuser:testpass" >> ~/.pgpass
chmod 600 ~/.pgpass
psql "postgresql://testuser@localhost:7777/postgres"
```

Result: Single connection (password cached)

### Option 2: Use PGPASSWORD environment variable

```bash
PGPASSWORD=testpass psql "postgresql://testuser@localhost:7777/postgres"
```

Result: Single connection (password provided upfront)

### Option 3: Inline password in connection string

```bash
psql "postgresql://testuser:testpass@localhost:7777/postgres"
```

Result: Single connection (password in URI)

### Option 4: Accept it as normal behavior

The first "failed" connection is expected and harmless. Your proxy is working correctly.

## Proxy Log Interpretation

```
✗ [62610] authentication relay failed: client disconnected: unexpected EOF
  ↑ This is EXPECTED - psql probing for auth method

✓ [62617] authentication completed successfully  
  ↑ This is the REAL connection after user entered password
```

## Testing

Compare direct PostgreSQL connection vs proxy:

```bash
# Direct to PostgreSQL (same behavior)
psql "postgresql://testuser@localhost:5432/postgres"
# You'll see the same pattern at the PostgreSQL server level

# Via proxy (same behavior)
psql "postgresql://testuser@localhost:7777/postgres"  
# Same pattern, just visible in proxy logs
```

## Conclusion

**This is NOT a bug.** The proxy is working correctly. The double connection is `psql` client behavior when prompting for passwords interactively.


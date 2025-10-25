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
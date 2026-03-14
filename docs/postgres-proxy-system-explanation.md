# Architecture

```text
                                          ┌───────────────────────────────────────┐
                                          │         LOCAL / OPERATOR SIDE         │
                                          │                                       │
                                          │  [start server]   [login]   [connect] │
                                          └───────────────┬───────────────┬───────┘
                                                          │               │
                                                          │               │ browser PKCE + token cache
                                                          │               v
                                                          │      ┌──────────────────────────────┐
                                                          │      │  Identity Provider / JWKS    │
                                                          │      │  - authorization endpoint    │
                                                          │      │  - token endpoint            │
                                                          │      │  - public signing keys       │
                                                          │      └──────────────────────────────┘
                                                          │
                                                          v
┌───────────────────────┐      optional TLS      ┌───────────────────────────────────────────────────────────┐      plain TCP      ┌────────────────────────────┐
│ PostgreSQL Client(s)  │ ---------------------> │                        PROXY                               │ -----------------> │      PostgreSQL Server     │
│                       │                        │                                                           │                    │                            │
│ - psql                │                        │  [Front Door / Listener]                                  │                    │ - auth rules               │
│ - apps/drivers        │                        │          |                                                │                    │ - backend sessions         │
│ - CLI bootstrap       │                        │          v                                                │                    │ - query execution          │
│                       │                        │  [Per-Client Session Handler]                             │                    │ - cancel processing        │
└───────────────────────┘                        │          |                                                │                    └────────────────────────────┘
                                                 │          +------------------------------+                 │
                                                 │          |                              |                 │
                                                 │          v                              v                 │
                                                 │  [Admission / Authentication]   [Cancel Registry]        │
                                                 │          |                              |                 │
                                                 │          |                              |                 │
                                                 │          v                              |                 │
                                                 │  [Disposable Auth Leg]                  |                 │
                                                 │          |                              |                 │
                                                 │          v                              |                 │
                                                 │  [Pool Manager] ------------------------+                 │
                                                 │          |                                                │
                                                 │          v                                                │
                                                 │  [Pinned Execution Leg For Session]                       │
                                                 │          |                                                │
                                                 │          v                                                │
                                                 │  [Bidirectional Query / Result Relay]                     │
                                                 └───────────────────────────────────────────────────────────┘


                                             ┌────────────────────────────────────────────────────────────────┐
                                             │                  THREE BACKEND RELATIONSHIPS                  │
                                             │                                                                │
                                             │  1) Disposable auth leg                                        │
                                             │     - opened during admission                                  │
                                             │     - proves credentials to database                           │
                                             │     - discarded before steady-state query flow                 │
                                             │                                                                │
                                             │  2) Execution leg                                              │
                                             │     - acquired from pool                                       │
                                             │     - held for full client session                             │
                                             │     - carries queries, results, errors, ready state           │
                                             │                                                                │
                                             │  3) Cancel leg                                                 │
                                             │     - short-lived side connection                              │
                                             │     - carries raw cancel packet only                           │
                                             │     - closes immediately after forwarding                      │
                                             └────────────────────────────────────────────────────────────────┘
```

## Startup

```text
[PROCESS START]
      |
      v
+--------------------------+
| initialize log settings  |
+--------------------------+
      |
      v
+--------------------------+
| parse CLI command        |
| - start server           |
| - login helper           |
| - connect helper         |
+--------------------------+
      |
      v
+------------------------------------------------------+
| load environment-backed runtime settings             |
| - proxy host / port                                  |
| - backend database host                              |
| - global pooled execution credentials                |
| - optional frontend TLS certificate/key              |
| - identity issuer / audience / key endpoint          |
| - role -> service account mappings                   |
+------------------------------------------------------+
      |
      v
                 +--------------------------------------+
                 | required identity settings present ? |
                 +-------------------+------------------+
                                     |
                      +--------------+--------------+
                      |                             |
                    [NO]                          [YES]
                      |                             |
                      v                             v
          +--------------------------+   +-------------------------------+
          | startup aborts           |   | initialize token validation   |
          | process exits            |   | and role mapping              |
          +--------------------------+   +-------------------------------+
                                                     |
                                                     v
                                      +-------------------------------+
                                      | load optional frontend TLS    |
                                      | - if cert/key missing: plain  |
                                      | - if present: TLS available   |
                                      +-------------------------------+
                                                     |
                                                     v
                                      +-------------------------------+
                                      | open listener on proxy port   |
                                      +-------------------------------+
                                                     |
                                                     v
                                      +-------------------------------+
                                      | [LISTENING]                   |
                                      | - accepts new TCP clients     |
                                      | - watches termination signal  |
                                      +-------------------------------+
```

## Login And Token Cache

```text
[LOGIN HELPER]
      |
      v
+--------------------------------------+
| existing cached credentials usable ? |
+------------------+-------------------+
                   |
         +---------+---------+
         |                   |
       [YES]               [NO]
         |                   |
         v                   v
+------------------+   +----------------------------------+
| skip new login   |   | generate PKCE verifier/challenge |
| keep cached auth |   | generate anti-forgery state      |
+------------------+   +----------------------------------+
                                |
                                v
                     +-------------------------------------+
                     | start loopback callback listener    |
                     | wait for browser redirect           |
                     +-------------------------------------+
                                |
                                v
                     +-------------------------------------+
                     | open browser to authorization page  |
                     +-------------------------------------+
                                |
                                v
                  +-------------------------------------------+
                  | user authenticates with identity provider |
                  +-------------------------------------------+
                                |
                                v
                    +---------------------------------------+
                    | callback returns authorization code   |
                    | state must match remembered value     |
                    +-------------------+-------------------+
                                        |
                         +--------------+--------------+
                         |                             |
                       [BAD]                         [GOOD]
                         |                             |
                         v                             v
              +-------------------------+   +-----------------------------+
              | reject callback         |   | exchange code for tokens    |
              | login fails             |   | access / refresh / ID token |
              +-------------------------+   +-----------------------------+
                                                          |
                                                          v
                                      +--------------------------------------+
                                      | decode minimal user identity claims  |
                                      | decode token roles if available      |
                                      +--------------------------------------+
                                                          |
                                                          v
                                      +--------------------------------------+
                                      | save credentials to local cache      |
                                      | with expiration timestamp            |
                                      +--------------------------------------+
                                                          |
                                                          v
                                               +------------------+
                                               | [LOGGED IN]      |
                                               +------------------+


[SUBSEQUENT CONNECT ATTEMPT]
      |
      v
+----------------------------------------+
| read cached credentials from disk      |
+----------------------------------------+
      |
      v
+----------------------------------------+
| token near expiry ?                    |
+--------------------+-------------------+
                     |
          +----------+----------+
          |                     |
        [NO]                  [YES]
          |                     |
          v                     v
+--------------------+   +-------------------------------+
| reuse access token |   | call token refresh endpoint   |
+--------------------+   +-------------------------------+
                                 |
                                 v
                    +------------------------------+
                    | refresh succeeds ?           |
                    +---------------+--------------+
                                    |
                      +-------------+-------------+
                      |                           |
                    [NO]                        [YES]
                      |                           |
                      v                           v
          +-----------------------------+   +-----------------------------+
          | re-login required           |   | overwrite local cache       |
          | no usable token             |   | continue with fresh token   |
          +-----------------------------+   +-----------------------------+
```

## Listener And Concurrency

```text
                                  ┌─────────────────────────────────────┐
                                  │          PROCESS LIFETIME           │
                                  └─────────────────────────────────────┘

   [termination watcher] ---------------------> [close listener on signal]

   [listener loop]
        |
        +--> accept client #1 --> [session handler #1] --> [own client socket]
        |
        +--> accept client #2 --> [session handler #2] --> [own client socket]
        |
        +--> accept client #3 --> [session handler #3] --> [own client socket]
        |
        `--> accept client #N --> [session handler #N] --> [own client socket]


                                  ┌─────────────────────────────────────┐
                                  │        SHARED PROCESS STATE         │
                                  └─────────────────────────────────────┘

   [JWKS/public-key cache] <---- consulted by token-auth sessions

   [role mapping table]   <---- consulted by token-auth sessions

   [pool manager]
        |
        +--> [pool for startup-user A + db X]
        |
        +--> [pool for startup-user B + db X]
        |
        `--> [pool for startup-user A + db Y]

   [cancel registry]
        |
        +--> execution PID/secret -> active session #1
        |
        +--> execution PID/secret -> active session #7
        |
        `--> execution PID/secret -> active session #12


                                  ┌─────────────────────────────────────┐
                                  │    PER-SESSION STATE WHILE ALIVE    │
                                  └─────────────────────────────────────┘

   [client socket]
   [requested startup identity]
   [target database]
   [frontend transport mode: TLS or plain]
   [disposable auth leg: temporary]
   [execution leg: pinned for session]
   [cancel identity: PID + secret]
   [session handler loop]
```

## Client Session Lifecycle

```text
[CLIENT TCP CONNECT]
      |
      v
+------------------------------+
| create session handler       |
| attach client socket         |
+------------------------------+
      |
      v
+------------------------------+
| receive startup-class input  |
+------------------------------+
      |
      v
          +------------------------------------------+
          | what kind of startup-class input arrived?|
          +--------------------+---------------------+
                               |
          +--------------------+------------------------------+
          |                    |                              |
        [TLS request]      [normal startup]             [cancel request]
          |                    |                              |
          v                    v                              v
 +------------------+   +--------------------+    +---------------------------+
 | TLS available ?  |   | begin admission    |    | resolve cancel target     |
 +--------+---------+   +--------------------+    | forward raw cancel packet  |
          |                                       | close side connection      |
    +-----+-----+                                 +---------------------------+
    |           |
  [NO]        [YES]
    |           |
    v           v
 +----------------------+   +---------------------------+
 | reject TLS upgrade   |   | perform TLS handshake     |
 | continue if client   |   | replace client transport  |
 | allows plaintext     |   | continue under TLS        |
 +----------------------+   +---------------------------+
            \                          /
             \                        /
              \                      /
               v                    v
              +----------------------+
              | receive normal       |
              | startup parameters   |
              +----------------------+
                         |
                         v
              +----------------------+
              | requested identity   |
              | requested database   |
              | client app name      |
              +----------------------+
                         |
                         v
              +----------------------+
              | open disposable auth |
              | leg to database      |
              +----------------------+
                         |
                         v
              +----------------------+
              | ask client to send   |
              | cleartext secret     |
              +----------------------+
                         |
                         v
              +----------------------+
              | receive secret       |
              +----------------------+
                         |
                         v
              +----------------------+
              | choose auth branch   |
              +----------------------+
                         |
                         v
              +----------------------+
              | authenticate against |
              | database on temp leg |
              +----------------------+
                         |
                         v
              +----------------------+
              | acquire execution    |
              | leg from pool        |
              +----------------------+
                         |
                         v
              +----------------------+
              | expose execution     |
              | cancel identity      |
              | send ready state     |
              +----------------------+
                         |
                         v
              +----------------------+
              | [PROXYING SESSION]   |
              +----------------------+
                         |
                         v
              +----------------------+
              | client terminates or |
              | socket breaks        |
              +----------------------+
                         |
                         v
              +----------------------+
              | rollback if needed   |
              | discard session data |
              | release execution leg|
              | unregister cancel id |
              +----------------------+
                         |
                         v
                    [CLOSED]
```

## Admission And Authentication

```text
                                         [CLIENT STARTUP RECEIVED]
                                                   |
                                                   v
                                   +-----------------------------------+
                                   | proxy requests cleartext secret   |
                                   | from client-facing connection     |
                                   +-----------------------------------+
                                                   |
                                                   v
                                   +-----------------------------------+
                                   | secret classification             |
                                   | token-like shape ?                |
                                   +----------------+------------------+
                                                    |
                             +----------------------+----------------------+
                             |                                             |
                           [YES]                                         [NO]
                             |                                             |
                             v                                             v
         +------------------------------------------------+   +--------------------------------------------+
         | TOKEN-BASED ADMISSION                          |   | PASSWORD-BASED ADMISSION                    |
         |                                                |   |                                            |
         | 1) validate signature with cached public key   |   | 1) keep requested startup identity         |
         | 2) verify issuer                               |   | 2) keep supplied password                  |
         | 3) verify audience                             |   | 3) use both on disposable auth leg        |
         | 4) verify expiration                           |   |                                            |
         | 5) require subject + email                     |   |                                            |
         | 6) extract role / roles                        |   |                                            |
         | 7) map role to database service identity       |   |                                            |
         | 8) use mapped identity on disposable auth leg  |   |                                            |
         +------------------------------------------------+   +--------------------------------------------+
                             |                                             |
                             +----------------------+----------------------+
                                                    |
                                                    v
                              +--------------------------------------------+
                              | database challenges disposable auth leg    |
                              +-------------------+------------------------+
                                                  |
                     +----------------------------+----------------------------+
                     |                             |                            |
                   [SCRAM]                       [MD5]                     [cleartext]
                     |                             |                            |
                     v                             v                            v
      +-------------------------------+  +-------------------------+  +-------------------------+
      | proxy performs multi-step     |  | proxy computes expected |  | proxy forwards actual   |
      | challenge/response exchange   |  | hash response           |  | secret on backend leg   |
      +-------------------------------+  +-------------------------+  +-------------------------+
                     \                             |                            /
                      \                            |                           /
                       \                           |                          /
                        +--------------------------+-------------------------+
                                                   |
                                                   v
                                    +-------------------------------+
                                    | backend admits session ?      |
                                    +---------------+---------------+
                                                    |
                                   +----------------+----------------+
                                   |                                 |
                                 [NO]                              [YES]
                                   |                                 |
                                   v                                 v
                     +-----------------------------+     +----------------------------------+
                     | send fatal auth failure     |     | forward auth success status      |
                     | close client session        |     | forward parameter/status data    |
                     +-----------------------------+     | do NOT expose temp cancel id     |
                                                         +----------------------------------+
                                                                          |
                                                                          v
                                                         +----------------------------------+
                                                         | proceed to execution-leg acquire |
                                                         +----------------------------------+


                             ┌─────────────────────────────────────────────────────────────────────┐
                             │ CURRENT BUILD BEHAVIOR DURING AUTH VS EXECUTION                    │
                             │                                                                     │
                             │ admission identity on disposable auth leg:                          │
                             │   - token path: mapped service identity                             │
                             │   - password path: requested startup identity                       │
                             │                                                                     │
                             │ steady-state execution identity on pooled leg:                      │
                             │   - global pooled execution account from runtime configuration       │
                             │                                                                     │
                             │ pool grouping key:                                                  │
                             │   - requested startup identity + target database                    │
                             └─────────────────────────────────────────────────────────────────────┘
```

## Execution Leg And Pooling

```text
                            [AUTH SUCCESSFUL ON DISPOSABLE LEG]
                                         |
                                         v
                         +-------------------------------------------+
                         | build target pool key                     |
                         | - requested startup identity              |
                         | - target database                         |
                         +-------------------------------------------+
                                         |
                                         v
                         +-------------------------------------------+
                         | pool exists for key ?                     |
                         +------------------+------------------------+
                                            |
                          +-----------------+------------------+
                          |                                    |
                        [NO]                                 [YES]
                          |                                    |
                          v                                    v
       +-------------------------------------------+   +------------------------------+
       | create pool lazily                        |   | reuse existing pool          |
       | - max connections                         |   |                              |
       | - idle timeout                            |   |                              |
       | - lifetime                                |   |                              |
       | - health checks                           |   |                              |
       +-------------------------------------------+   +------------------------------+
                          \                                    /
                           \                                  /
                            v                                v
                         +-------------------------------------------+
                         | acquire one execution connection          |
                         | from pool                                |
                         +-------------------------------------------+
                                         |
                                         v
                         +-------------------------------------------+
                         | ping execution connection                 |
                         | acquisition valid ?                       |
                         +------------------+------------------------+
                                            |
                        +-------------------+--------------------+
                        |                                        |
                      [NO]                                     [YES]
                        |                                        |
                        v                                        v
       +-----------------------------------------+   +--------------------------------------+
       | release failed acquisition              |   | pin execution connection to client   |
       | tell client database unavailable        |   | session for entire session lifetime  |
       | close session                           |   +--------------------------------------+
       +-----------------------------------------+                      |
                                                                        v
                                                     +--------------------------------------+
                                                     | read execution cancel identity       |
                                                     | expose it to client                  |
                                                     | register it in cancel registry       |
                                                     +--------------------------------------+
                                                                        |
                                                                        v
                                                     +--------------------------------------+
                                                     | [EXECUTION LEG IN USE]               |
                                                     +--------------------------------------+
                                                                        |
                                                                        v
                                                     +--------------------------------------+
                                                     | on session end:                      |
                                                     | - rollback                           |
                                                     | - discard session state              |
                                                     | - release back to pool               |
                                                     +--------------------------------------+


                               ┌──────────────────────────────────────────────────────────────┐
                               │ POOLING SEMANTICS                                           │
                               │                                                              │
                               │ - one execution connection pinned to one client session      │
                               │ - no transaction multiplexing across active sessions         │
                               │ - reuse happens only after client disconnect                 │
                               │ - concurrency bounded by per-pool connection limit           │
                               └──────────────────────────────────────────────────────────────┘
```

## Query / Response Relay

```text
                           [SESSION READY FOR QUERY]
                                      |
                                      v
                     +------------------------------------+
                     | wait for one client protocol unit  |
                     +------------------------------------+
                                      |
                                      v
                     +------------------------------------+
                     | client message type                |
                     | - simple query                     |
                     | - parse / bind / describe / exec   |
                     | - sync                             |
                     | - terminate                        |
                     | - other frontend message           |
                     +------------------------------------+
                                      |
                                      v
                     +------------------------------------+
                     | forward client message to          |
                     | pinned execution leg              |
                     +------------------------------------+
                                      |
                                      v
                     +------------------------------------+
                     | enter backend response loop        |
                     +------------------------------------+
                                      |
                                      v
                     +------------------------------------+
                     | receive backend message            |
                     +------------------------------------+
                                      |
                                      v
                     +------------------------------------+
                     | forward backend message to client  |
                     +------------------------------------+
                                      |
                                      v
                     +------------------------------------+
                     | backend message class              |
                     +----------------+-------------------+
                                      |
          +---------------------------+-----------------------------------------------+
          |                           |                         |                     |
        [row/result]              [command done]          [error response]      [ready-for-query]
          |                           |                         |                     |
          v                           v                         v                     v
 +---------------------+   +---------------------+   +---------------------+   +----------------------+
 | keep relaying       |   | keep relaying       |   | relay error to      |   | end current relay    |
 | until readiness     |   | until readiness     |   | client, then keep   |   | cycle, await next    |
 | comes back          |   | comes back          |   | relaying until ready |   | client command       |
 +---------------------+   +---------------------+   +---------------------+   +----------------------+


         [terminate from client]
                 |
                 v
     +---------------------------+
     | stop relay loop           |
     | begin session cleanup     |
     +---------------------------+
```

## Cancel Flow

```text
                               [NORMAL SESSION ALREADY RUNNING]
                                            |
                                            v
                     +----------------------------------------------+
                     | client previously received execution-leg     |
                     | process identifier + cancel secret           |
                     +----------------------------------------------+
                                            |
                                            v
                           user interrupts query / driver cancels
                                            |
                                            v
                     +----------------------------------------------+
                     | client opens separate side connection        |
                     +----------------------------------------------+
                                            |
                                            v
                     +----------------------------------------------+
                     | client sends cancel packet                   |
                     | containing process id + cancel secret        |
                     +----------------------------------------------+
                                            |
                                            v
                     +----------------------------------------------+
                     | proxy checks cancel registry                 |
                     | execution identity known ?                   |
                     +------------------+---------------------------+
                                        |
                    +-------------------+-------------------+
                    |                                       |
                  [NO]                                    [YES]
                    |                                       |
                    v                                       v
      +------------------------------------+   +--------------------------------------+
      | reject/ignore as unknown target    |   | open short-lived backend side socket |
      | close side connection              |   +--------------------------------------+
      +------------------------------------+                     |
                                                                 v
                                              +--------------------------------------+
                                              | write raw cancel packet to database  |
                                              | close side socket immediately        |
                                              +--------------------------------------+
                                                                 |
                                                                 v
                                              +--------------------------------------+
                                              | database matches process id/secret ? |
                                              +------------------+-------------------+
                                                                 |
                                           +---------------------+---------------------+
                                           |                                           |
                                         [NO]                                        [YES]
                                           |                                           |
                                           v                                           v
                        +----------------------------------+         +----------------------------------------+
                        | nothing changes on active query  |         | running query is interrupted          |
                        | session eventually continues     |         | active session receives db error      |
                        +----------------------------------+         | active session returns to ready state |
                                                                     +----------------------------------------+
```

## Failure And Alternate Paths

```text
                                        [FAILURE MAP]

   startup time
   ------------
   missing config --------------------------> [PROCESS DOES NOT START]
   invalid identity bootstrap --------------> [PROCESS DOES NOT START]
   invalid role mapping --------------------> [PROCESS DOES NOT START]
   listener bind failure -------------------> [PROCESS DOES NOT START]


   client admission
   ----------------
   client disconnects before secret --------> [SESSION CLOSED EARLY]
   TLS handshake failure -------------------> [SESSION CLOSED]
   token validation failure ---------------> [FATAL AUTH ERROR TO CLIENT] -> [CLOSED]
   no mapped role -------------------------> [ACCESS DENIED TO CLIENT] ----> [CLOSED]
   backend unreachable during auth --------> [BACKEND UNAVAILABLE] ---------> [CLOSED]
   backend rejects credentials ------------> [AUTH FAILURE RELAYED] --------> [CLOSED]


   execution-leg creation
   ----------------------
   pool creation failure ------------------> [DATABASE UNAVAILABLE] --------> [CLOSED]
   acquisition failure --------------------> [DATABASE UNAVAILABLE] --------> [CLOSED]
   ping failure after acquire -------------> [RELEASE + CLOSE]


   steady-state relay
   ------------------
   client socket EOF ----------------------> [CLEANUP]
   backend socket error -------------------> [CLEANUP]
   query error from database -------------> [ERROR TO CLIENT] -> [WAIT FOR READY]
   cancel request miss --------------------> [NO EFFECT ON ACTIVE SESSION]


   cleanup
   -------
   rollback fails -------------------------> [BEST-EFFORT CONTINUES]
   discard state fails --------------------> [BEST-EFFORT CONTINUES]
   release still happens ------------------> [CONNECTION RETURNS TO POOL]


   client-side interactive probe case
   ----------------------------------
   probe connection to discover auth -----> [CLIENT DISCONNECTS]
                                             |
                                             v
                                   [USER PROMPT / CREDENTIAL PREP]
                                             |
                                             v
                                   [SECOND CONNECTION DOES REAL LOGIN]
```

## Shutdown

```text
[PROCESS RUNNING]
      |
      v
+-------------------------------+
| termination signal arrives    |
+-------------------------------+
      |
      v
+-------------------------------+
| close listener                |
| stop accepting new clients    |
+-------------------------------+
      |
      v
+---------------------------------------------+
| existing session handlers still in progress  |
| - some may finish naturally                  |
| - no new admission begins                    |
+---------------------------------------------+
      |
      v
+---------------------------------------------+
| process attempts best-effort wind-down       |
+---------------------------------------------+
      |
      v
+---------------------------------------------+
| each ending session performs cleanup         |
| - unregister cancel identity                 |
| - rollback unfinished work                   |
| - discard session state                      |
| - release execution leg to pool              |
+---------------------------------------------+
      |
      v
   [PROCESS EXIT]


               ┌─────────────────────────────────────────────────────────────┐
               │ SHUTDOWN CHARACTER                                          │
               │                                                             │
               │ - listener-first stop                                       │
               │ - new clients blocked                                       │
               │ - active sessions may continue briefly                      │
               │ - shutdown should be read as best-effort, not deep drain    │
               └─────────────────────────────────────────────────────────────┘
```

## State Machine

```text
   [CREATED]
       |
       v
   [ACCEPTED]
       |
       +--> [TLS NEGOTIATION]
       |          |
       |          +--> success --> [STARTUP READ]
       |          |
       |          `--> failure --> [FAILED / CLOSED]
       |
       `--> [STARTUP READ]
                  |
                  +--> cancel packet -----------> [CANCEL SIDE PATH] --> [CLOSED]
                  |
                  `--> normal startup ----------> [AWAIT SECRET]
                                                     |
                                                     +--> client disconnect --> [CLOSED]
                                                     |
                                                     +--> token branch -------> [TOKEN VALIDATION]
                                                     |                               |
                                                     |                               +--> failure --> [FAILED / CLOSED]
                                                     |                               |
                                                     |                               `--> success
                                                     |
                                                     `--> password branch ----> [PASSWORD ADMISSION]
                                                                                     |
                                                                                     `--> continue
                                                                                              |
                                                                                              v
                                                                                  [BACKEND AUTH]
                                                                                     |
                                                                                     +--> failure --> [FAILED / CLOSED]
                                                                                     |
                                                                                     `--> success --> [ACQUIRE EXECUTION LEG]
                                                                                                           |
                                                                                                           +--> failure --> [FAILED / CLOSED]
                                                                                                           |
                                                                                                           `--> success --> [READY]
                                                                                                                               |
                                                                                                                               v
                                                                                                                           [PROXYING]
                                                                                                                               |
                                                                                  +--------------------------------------------+--------------------------------------------+
                                                                                  |                                            |                                            |
                                                                                  |                                            |                                            |
                                                                          [query / response cycle]                    [cancel handled in side path]                 [client terminate / EOF]
                                                                                  |                                            |                                            |
                                                                                  +-------------------------------> [READY / PROXYING] <----------------------------+
                                                                                                                                                 |
                                                                                                                                                 v
                                                                                                                                             [CLEANUP]
                                                                                                                                                 |
                                                                                                                                                 v
                                                                                                                                              [CLOSED]
```

## Full End-To-End View

```text
   ┌──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
   │                                                   FULL SYSTEM LIFECYCLE                                                               │
   └──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘

   [BOOT]
      |
      v
   load config --> init identity validation --> load optional TLS --> bind listener --> [LISTENING]
                                                                                               |
                                                                                               v
                                                                                accept client connection
                                                                                               |
                                                                                               v
                                                                      TLS request? ---- yes --> negotiate TLS
                                                                                               |
                                                                                               no / after TLS
                                                                                               |
                                                                                               v
                                                                                       receive startup
                                                                                               |
                                                                                               v
                                                                                request cleartext secret
                                                                                               |
                                                                                               v
                                                                      token-like? ---- yes --> validate token
                                                                           |                   verify issuer/audience/exp
                                                                           |                   map roles to db identity
                                                                           |
                                                                           no
                                                                           |
                                                                           v
                                                                    use requested db identity
                                                                                               |
                                                                                               v
                                                                             authenticate on disposable backend leg
                                                                                               |
                                                                                               +---- fail --> send fatal error --> [CLOSED]
                                                                                               |
                                                                                               v
                                                                               acquire pinned execution leg from pool
                                                                                               |
                                                                                               +---- fail --> send unavailable --> [CLOSED]
                                                                                               |
                                                                                               v
                                                                      send execution cancel identity + ready-for-query
                                                                                               |
                                                                                               v
                                                                                         [PROXYING]
                                                                                               |
                                                                                               +---- client message --> backend --> results/errors --> client
                                                                                               |
                                                                                               +---- side cancel --> registry lookup --> raw cancel packet --> db
                                                                                               |
                                                                                               +---- client EOF / terminate
                                                                                               v
                                                                                            [CLEANUP]
                                                                                               |
                                                                                               v
                                                               rollback -> discard session state -> release execution leg -> unregister cancel id
                                                                                               |
                                                                                               v
                                                                                            [CLOSED]
                                                                                               |
                                                                                               v
                                                                                      listener continues for others
                                                                                               |
                                                                                               v
                                                                                termination signal closes listener
                                                                                               |
                                                                                               v
                                                                                            [EXIT]
```

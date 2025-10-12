## OAuth 2.0 and OIDC: Concepts and Flows

### Overview: OAuth 2.0
OAuth 2.0 is an industry-standard authorization framework that allows a user to grant a third-party application limited access to protected resources without sharing their credentials. It focuses on delegated authorization, not user authentication.

### Problems OAuth 2.0 Addresses
- Credential exposure: Eliminates the need to share usernames/passwords with third parties.
- Scoped access: Grants only the minimal required permissions via scopes.
- Revocation: Enables revoking access for one application without changing the user’s password.

### High-Level OAuth 2.0 Flow
1. The application requests authorization from the user.
2. If granted, the application receives an authorization grant.
3. The application exchanges the grant (plus its own identity) for an access token.
4. If valid, the authorization server issues an access token (and optionally a refresh token).
5. The application calls the resource server with the access token.
6. The resource server validates the token and serves the requested resources.

![OAuth 2.0 overview](https://blog.postman.com/wp-content/uploads/2023/08/OAuth-2.0.png)

### Common Grant Types
- Authorization Code: For server-side applications; most widely used.
- Client Credentials: For machine-to-machine access using the application’s own identity.
- Device Code: For devices without a browser or with limited input capabilities.

---

## Authorization Code Flow (most common)

![Authorization Code flow](https://assets.digitalocean.com/articles/oauth/auth_code_flow.png)

Step 1 — Authorization request link example:
https://cloud.digitalocean.com/v1/oauth/authorize?response_type=code&client_id=CLIENT_ID&redirect_uri=CALLBACK_URL&scope=read

- Authorization endpoint: https://cloud.digitalocean.com/v1/oauth/authorize  
- client_id: The application’s identifier.  
- redirect_uri: Where the service redirects after authorization.  
- response_type=code: Indicates the authorization code flow.  
- scope=read: Requested permission scope.

Step 2 — User authorizes the application  
The user logs in (if needed) and approves or denies access.

![Authorize application prompt](https://assets.digitalocean.com/articles/oauth/authcode.png)

Step 3 — Application receives authorization code  
Example redirect:
https://dropletbook.com/callback?code=AUTHORIZATION_CODE

Step 4 — Application exchanges code for token  
Example token request:
https://cloud.digitalocean.com/v1/oauth/token?client_id=CLIENT_ID&client_secret=CLIENT_SECRET&grant_type=authorization_code&code=AUTHORIZATION_CODE&redirect_uri=CALLBACK_URL

Step 5 — Application receives access token (and optionally refresh token)  
Example response:
{"access_token":"ACCESS_TOKEN","token_type":"bearer","expires_in":2592000,"refresh_token":"REFRESH_TOKEN","scope":"read","uid":100101,"info":{"name":"Mark E. Mark","email":"mark@thefunkybunch.com"}}

---

## Client Credentials Flow
Used when an application needs to access its own resources (no end-user involved).

Example request:
https://oauth.example.com/token?grant_type=client_credentials&client_id=CLIENT_ID&client_secret=CLIENT_SECRET

If valid, the authorization server returns an access token the application can use for its own account.

---

## Device Code Flow
Used by devices without a browser or with limited input.

1. The device requests a device code from the device authorization endpoint.  
   Example:
   POST https://oauth.example.com/device  
   client_id=CLIENT_ID

2. The response includes a device_code, user_code, verification_uri, interval, and expires_in:
{
  "device_code": "IO2RUI3SAH0IQuESHAEBAeYOO8UPAI",
  "user_code": "RSIK-KRAM",
  "verification_uri": "https://example.okta.com/device",
  "interval": 10,
  "expires_in": 1600
}

3. The user visits the verification_uri on another device, enters the user_code, and signs in.
4. Meanwhile, the device polls the token endpoint until the user authorizes or the code expires.
5. On approval, the authorization server returns an access token to the device.

---

## OpenID Connect (OIDC)

### Why OIDC?
OAuth 2.0 provides authorization, not authentication. It does not standardize identity claims, user info, or how to verify who the user is. OIDC adds an identity layer on top of OAuth 2.0 to safely authenticate users.

### What OIDC Adds
- ID Token (JWT) with standardized identity claims (e.g., sub, email, name) and validation fields (iss, aud, exp).
- UserInfo endpoint for additional profile attributes.
- Discovery via well-known configuration.
- The openid scope indicating an authentication request.

In practice, a successful OIDC flow:
- Includes an OAuth 2.0 access token.
- Returns an ID token (JWT) with identity information.
- Uses OAuth 2.0 transport and error semantics.

![OIDC overview](https://awsfundamentals.com/_next/image?url=%2Fassets%2Fblog%2Foidc-introduction%2F2490ce7e-1759-4ebb-8b57-81467fcb21f6.webp&w=3840&q=75)

### OAuth 2.0 vs OIDC (summary)
- Purpose:  
  - OAuth 2.0: Delegate access to resources.  
  - OIDC: Authenticate the user (plus optional resource access).
- Tokens:  
  - OAuth 2.0: Access Token (opaque or JWT).  
  - OIDC: Access Token + ID Token (JWT).
- Standardization:  
  - OAuth 2.0: No standard identity claims.  
  - OIDC: Standard claims and discovery.
- Login use case:  
  - OAuth 2.0 alone: Not sufficient.  
  - OIDC: Safe, standardized login.

Developers started using OAuth tokens as proof of identity (“if I have a token, I must be that user”).
But OAuth never guaranteed identity — the token is just a random opaque string meant for an API.

So, when people tried to use OAuth for authentication (login), it caused:

Inconsistent implementations

Security holes (no standard claims like “email” or “name”)

No standard endpoint to fetch user info

No way to verify the token’s issuer reliably

The Solution: OpenID Connect (OIDC)

OIDC was introduced as a formal identity layer on top of OAuth 2.0.
It adds the missing “who is the user?” piece.

It adds:

ID Token (JWT) → a standardized, cryptographically signed token containing identity info:

sub (subject / user ID)

email, name, etc.

iss, aud, exp for validation

UserInfo endpoint → API to fetch extra user profile data

Standard discovery mechanism → .well-known/openid-configuration endpoint

New scope: openid → signals “I want identity info”

So now, you can safely:

Authenticate the user (via ID Token)

Authorize access to your resource (via Access Token)

If you used only OAuth 2.0, you’d only get:

A token that says, “This client can access the Postgres proxy.”

You wouldn’t know:

Who the user is

What their email or groups are

How to enforce RBAC securely

OIDC adds those missing identity pieces (via ID Token & claims).
---

## PKCE (Proof Key for Code Exchange)

### Why PKCE?
Public clients (native apps and SPAs) cannot safely store a client secret. PKCE strengthens the Authorization Code flow against code interception by binding the authorization request to the token exchange using a one-time verifier.

### How PKCE Works
1. The client creates a cryptographically random code_verifier and derives a code_challenge.
2. The client starts the authorization flow, sending the code_challenge with the authorization request.
3. After the user authenticates and grants consent, the app receives an authorization code.
4. The client exchanges the authorization code for tokens, sending the original code_verifier.
5. The authorization server validates that the code_verifier matches the code_challenge before issuing tokens.

![Authorization Code with PKCE](https://images.ctfassets.net/cdy7uua7fh8z/3pstjSYx3YNSiJQnwKZvm5/33c941faf2e0c434a9ab1f0f3a06e13a/auth-sequence-auth-code-pkce.png)

If Refresh Token Rotation is enabled, a new refresh token is issued with each exchange; the previous one is invalidated while the server maintains linkage for detection and revocation.

---

## Key Takeaways
- Use OAuth 2.0 for delegated authorization (access to resources).
- Use OIDC when you need to authenticate users and obtain standardized identity claims.
- Prefer Authorization Code with PKCE for public clients (native/SPA).
- Use Device Code for limited-input devices; use Client Credentials for machine-to-machine scenarios.
- Rely on discovery documents and JWKS for correct validation of tokens and issuers.



Once the auth0 setup is done refer to the tenant.yaml in  for auth0/tenant.yaml for tenant configurations 

I need to implement OIDC PKCE flow in my proxy when user type gprxy login hence 

Here’s what your CLI must handle:

Generate PKCE parameters

code_verifier → random secure string
Must be a high-entropy cryptographically random string

Length: 43–128 characters

Character set: [A–Z, a–z, 0–9, "-", ".", "_", "~"]

Must be base64url-safe (no =, +, /)

code_challenge → base64(SHA256(code_verifier))

Used to protect against token interception.

state : 

state

A random string used to maintain state between request and callback — for security.

It prevents CSRF (Cross-Site Request Forgery) attacks.

When you start the OAuth flow, your app generates a random state value and remembers it (e.g., in session).
When Auth0 redirects back to your app’s redirect_uri, it includes that same state value.

✅ If they match → it’s a legitimate response.
🚫 If not → reject it (could be a malicious redirect).


Build the Auth0 authorization URL
Example:

https://<AUTH0-DOMAIN>/authorize?
    response_type=code&
    code_challenge={codeChallenge}&
    code_challenge_method=S256&
    client_id=<CLIENT-ID>&
    redirect_uri={yourCallbackUrl}&
    scope=SCOPE&
    audience={apiAudience}&
    state={state}


Open a browser window

Launch the URL in the user’s default browser (exec.Command("open", url) on macOS or xdg-open on Linux).

The user logs in (via Azure AD through Auth0).

Start a tiny local webserver on the CLI

Listen on localhost:8085/callback.

Capture the code query parameter when Auth0 redirects the user back after login.

Exchange the code for tokens

Make a POST request to https://<AUTH0_DOMAIN>/oauth/token with:

{
  "grant_type": "authorization_code",
  "client_id": "<CLIENT_ID>",
  "code_verifier": "<code_verifier>",
  "code": "<AUTH_CODE>",
  "redirect_uri": "http://localhost:8085/callback"
}


Auth0 returns:

{
  "access_token": "eyJhbGciOi...",
  "id_token": "eyJhbGciOi...",
  "refresh_token": "eyJhbGciOi...",
  "token_type": "Bearer",
  "expires_in": 86400
}


Save the tokens locally

Store the tokens securely in a local file (e.g. ~/.gprxy/config.json):

{
  "access_token": "...",
  "id_token": "...",
  "refresh_token": "...",
  "expires_at": "2025-10-11T15:00:00Z"
}


Validate and print success

Optionally decode the ID token using JWT library to show:

Logged in as user@email.com (role: admin)


Store minimal info in config.
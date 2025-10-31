# gprxy Documentation

This folder contains the technical documentation for the PostgreSQL proxy.

## Table of contents

- User guide
  - [Comprehensive User Guide](./user-guide.md)

- Overview and architecture
  - [Architecture and Code Flow](./architecture.md)

- Authentication
  - [SCRAM Authentication Implementation](./auth-scram.md)
  - [OAuth 2.0 and OIDC Integration](./authentication-oauth-oidc.md)

- Connections and protocol
  - [Connection Behavior (psql double connections)](./connection-behavior.md)
  - [TLS/SSL Implementation](./tls.md)
  - [Cancel Request Handling](./cancel-requests.md)

- Operations
  - [Logging](./logging.md)
  - [CLI Usage](./cli.md)

- Engineering notes
  - [Engineering Notes and Changelog](./go.doc.md)
  - Long-form deep dive: [gpt-analysis.md](./gpt-analysis.md)

Notes
- Content has been consolidated and filenames simplified for clarity. Historical files were merged where appropriate without changing technical meaning.


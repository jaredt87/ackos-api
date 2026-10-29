# ackos-api

A small authenticated HTTP adapter for the ackOS control runtime. The API lets a caller identify an allowlisted repository, branch, and existing tracked file; ackOS obtains the actual observation and returns a signed, short-lived observation token. A change request is bound to that token and is rejected if the resource fingerprint, version, or concurrency token has changed.

The API delegates execution to the existing ackOS `control.Host` and Git `FileProvider`. It does not use GitHub write APIs and does not implement a second execution or verification engine.

## Current scope

- Observe an existing regular file on an allowlisted local Git repository and branch.
- Replace that file's bytes and mode through the ackOS Git provider.
- Verify the resulting commit and target state through the provider before reporting a committed result.
- Reject stale or expired observation tokens.
- Require bearer authentication for mutation-related endpoints.
- Accept repository roots only from server configuration; request bodies cannot select arbitrary filesystem roots.

The initial version is intentionally limited to replacing existing files. It does not create or delete files, run Terraform, provision cloud resources, push to GitHub, or merge branches.

## Run

Requires Go 1.25+ and a local checkout of each configured repository.

```bash
export ACKOS_API_BEARER_TOKEN='replace-with-a-long-random-client-token'
export ACKOS_API_TOKEN_SECRET='replace-with-at-least-32-random-bytes'
export ACKOS_REPOSITORIES='{"terraform":"/srv/repos/terraform-aws-postgres-db"}'
export ACKOS_API_ADDR='127.0.0.1:8080'
go run ./cmd/ackos-api
```

Keep the bearer token and signing key secret. Terminate TLS at a trusted reverse proxy or serve behind an authenticated private network. Do not expose this service directly to the public internet without TLS, rate limiting, operational monitoring, and an appropriate identity/authorization policy.

## Routes

### `GET /healthz`

Returns service liveness. This endpoint does not reveal configured repository paths.

### `POST /v1/observations`

Requires `Authorization: Bearer <token>`.

```json
{
  "repository_id": "terraform",
  "branch": "main",
  "path": "README.md"
}
```

Returns a short-lived opaque `observation_token`. The caller never constructs a Git fingerprint or branch-tip token.

### `POST /v1/changes`

Requires `Authorization: Bearer <token>`.

```json
{
  "observation_token": "<token returned by /v1/observations>",
  "mode": "100644",
  "content_base64": "IyBFeGFtcGxlCg=="
}
```

Content must be canonical padded Base64. Only regular-file modes `100644` and `100755` are accepted. A successful response means ackOS's provider verification succeeded and the kernel committed the transition. A stale observation returns HTTP 409 and requires a new inspection.

See [openapi.yaml](openapi.yaml) for the contract.

## Important lifecycle limitation

The current ackOS kernel lifecycle is in-memory. This API keeps per-resource runtime sessions in process memory and fails a change request if its observation session is unavailable. It does not yet persist recovery state across a process restart. Treat this initial version as a controlled integration/testing service, not a highly available production control plane; durable operation/recovery storage must be added before deploying where restart recovery is a requirement.

## Development

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
```

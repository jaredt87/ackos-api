# AGENTS.md — ackos-api

## Purpose and licensing

This repository is the authenticated HTTP adapter for the ackOS control runtime. It delegates resource execution and verification to ackOS; it is not a separate control kernel or authorization engine.

- This repository is licensed under Apache License 2.0. Read `LICENSE`; these instructions do not modify or add restrictions to the license.
- Read `README.md` and `openapi.yaml` before making changes. Read the relevant ackOS integration and architecture documentation when behavior crosses the API/kernel boundary.
- Keep API behavior aligned with the documented contract and the existing ackOS `control.Host` and provider implementation.

## Required boundaries

- Do not implement a second execution, authorization, verification, recovery, or commitment engine in the API.
- Delegate mutations to ackOS. Never substitute direct Git writes, GitHub write APIs, shell-driven Git mutation, or a synthetic execution path for the established control/provider path.
- Keep repository roots server-configured and allowlisted. Never accept arbitrary filesystem roots from request bodies or expand access beyond the configured resource scope.
- Preserve authentication, observation-token binding, expiry, freshness, concurrency checks, and file-mode/content validation. Do not weaken them to ease pilot setup.
- Keep credentials, bearer tokens, signing keys, and other secrets out of commits, logs, error responses, documentation, and test fixtures. Use safe local configuration and redacted examples.
- Do not expose this service to the public internet without an explicit deployment design for TLS, authentication, rate limiting, monitoring, and access policy.
- The current lifecycle/session behavior and restart limitations documented in README are important. Do not claim durability or restart recovery that is not implemented and tested.

## Pilot setup and testing

- For pilots, use a disposable, explicitly allowlisted repository and an existing tracked regular file. Keep tests comment/documentation-only unless a pilot owner explicitly authorizes a different test scope and the system supports it.
- Do not run Terraform apply, provision cloud resources, push branches, merge PRs, or make production changes as part of ordinary setup or validation.
- When a pilot workflow or capability is not supported, stop and report the limitation rather than bypassing ackOS.
- Report outcomes precisely: attempted, blocked, partial, verified, and committed are distinct states. Claim verified/committed only when the API/ackOS response and evidence support that claim.

## Development verification

For changes, run the repository's documented checks:

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
```

Do not omit checks or claim they passed unless they were actually run. Keep PRs scoped, document behavior changes, and add regression tests for boundary-related changes.

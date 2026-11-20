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


## ackOS Ecosystem Routing

This repository is part of the ackOS ecosystem:

- **ackOS** — https://github.com/jaredt87/ackOS: the execution-control kernel. Owns authorization, governed execution, independent verification, recovery, and commitment.
- **ackos-api** — https://github.com/jaredt87/ackos-api: the authenticated HTTP interface to ackOS. Delegates execution and verification to ackOS.
- **Wingman** — https://github.com/jaredt87/wingman: shared agent context, instructions, skills, and integration workflows.

### Related repository instructions

Consult the other repositories' agent instructions when a task crosses repository boundaries, changes an integration contract, modifies shared agent workflows, or depends on another component's behavior:

- ackOS instructions: https://github.com/jaredt87/ackOS/blob/main/AGENTS.md
- ackos-api instructions: https://github.com/jaredt87/ackos-api/blob/main/AGENTS.md
- Wingman instructions: https://github.com/jaredt87/wingman/blob/main/AGENTS.md

### Cross-repository coordination

- Read the local instructions of every repository affected by a cross-repository task.
- ackOS remains the execution and control authority; do not create parallel authorization, execution, verification, recovery, or commitment paths.
- ackos-api is an interface and adapter, not a competing control engine.
- Wingman supplies shared agent context and workflows; it must not bypass ackOS for changes designated to be governed through ackOS.
- Identify contract/dependency changes explicitly. Keep changes independently reviewable where practical.
- Verify each affected repository using its own documented checks and report incomplete work, assumptions, and dependencies accurately.

This routing section is an ecosystem navigation and coordination layer. It does not override repository-specific architecture, security, development, or agent instructions. If instructions conflict, stop and resolve the conflict.

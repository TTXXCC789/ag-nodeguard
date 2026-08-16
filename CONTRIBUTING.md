# Contributing

Thank you for helping improve Anti Gravity NodeGuard. Contributions must preserve its narrow routing and privacy guarantees.

## Development setup

Requirements: macOS 14+, Go 1.26+, Apple Command Line Tools/Xcode, and Swift 6. Runtime integration testing additionally requires compatible local installations of Clash Verge Rev and Anti Gravity.

```sh
go test ./...
go test -race ./...
go vet ./...
./macos-app/scripts/test-swift.sh
GO_BIN="$(command -v go)" ./macos-app/scripts/build-app.sh
```

## Design constraints

- Never switch Clash Verge's global/current profile to implement an Anti Gravity change.
- Never use mouse, AppleScript, screenshot, or accessibility automation as a Retry fallback.
- Never log or return subscription URLs, proxy credentials, CSRF values, prompts, responses, or full conversation IDs.
- Treat profile YAML, local APIs, logs, Connect-RPC, and CDP output as untrusted/versioned input.
- Validate and roll back before replacing a running configuration.
- Preserve the one-task/node Retry boundary, switch rate limit, and failure deduplication.
- Keep CLI invocations as fixed argument arrays; do not pass user strings through a shell.

## Pull requests

1. Open an issue first for behavior that changes routing, retry semantics, or supported application versions.
2. Keep changes focused and include regression tests.
3. Run the full verification commands above.
4. Explain privacy/security impact and the rollback path.
5. Use synthetic fixtures. Never attach a real subscription, config, log, or conversation.

By contributing, you agree that your contribution is licensed under the repository's MIT License.

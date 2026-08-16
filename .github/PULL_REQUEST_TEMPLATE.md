## What changed

<!-- Explain the behavior change and why it is needed. -->

## Safety and compatibility

- Routing scope:
- Sensitive local data impact:
- Retry/quota impact:
- Version/API compatibility:
- Rollback behavior:

## Verification

- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] `./macos-app/scripts/test-swift.sh`
- [ ] Release app build and signature verification, if UI/build code changed
- [ ] Fixtures and diagnostics contain no real subscriptions, credentials, IPs, paths, logs, or conversation content

# Security Policy

## Supported version

Security fixes are provided for the latest release line, currently `0.5.x`. Compatibility fixes may require the exact tested versions listed in the README.

## Reporting a vulnerability

Use GitHub's **Report a vulnerability** / Private Vulnerability Reporting feature for this repository. Do not open a public issue for a security problem.

Include:

- NodeGuard, macOS, Clash Verge Rev, Mihomo, and Anti Gravity versions.
- A minimal reproduction using synthetic or redacted data.
- Expected impact and whether the issue requires local access.

Never include subscription URLs, proxy credentials, access tokens, CSRF values, complete profile files, complete logs, personal paths, conversation IDs, prompts, or model output.

## Sensitive local data

NodeGuard reads local Clash profile proxy definitions because Mihomo requires complete definitions in the dedicated managed runtime. The program is designed to:

- Keep those definitions on the local Mac.
- Write runtime/config/state/log files with mode `0600` and directories with mode `0700`.
- Expose only redacted subscription metadata and node display names through public JSON.
- Avoid analytics, telemetry, remote crash reporting, or an update service.
- Accept a TCP external controller only on loopback and only when a secret is configured; Unix Socket is preferred.

## Trust boundaries

Clash profile indexes and YAML files are untrusted input. NodeGuard rejects unsafe UIDs, traversal, unexpected profile extensions, symlinks, and files outside the expected profiles directory. State-changing rebuilds use validation, atomic replacement, snapshots, and rollback.

The CDP and Connect-RPC integrations are local but version-sensitive. They may invoke an existing Retry action, which can re-submit a model task and consume quota. NodeGuard deduplicates and rate-limits retries, and stops when task identity or UI action semantics are ambiguous.

## Release binaries

GitHub Release apps are Apple Silicon builds, ad-hoc signed, and not notarized with an Apple Developer ID. Verify the downloaded archive against `SHA256SUMS.txt`. Users requiring a stronger provenance guarantee should build from the tagged source.

Maintainers must scan source and release artifacts for credentials, subscription endpoints, personal paths, and real IP addresses before publication.

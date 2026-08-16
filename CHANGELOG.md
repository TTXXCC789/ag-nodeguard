# Changelog

All notable changes are documented here. The project follows semantic versioning from the first public release.

## [0.5.0] - 2026-08-16

### Added

- Native SwiftUI menu-bar app and live dashboard.
- Background status stream, candidate table, health state, cooldowns, and service controls.
- Automatic discovery of direct-node Clash Verge remote subscriptions.
- Preferred-subscription selection and cross-subscription fallback.
- Subscription-scoped node names, statistics, exit history, and cooldowns.
- Transactional managed runtime and Proxies/Groups/Rules enhancements for remote profiles.
- Background Agent failure detection and bounded CDP Retry.
- CLI interfaces for status streaming, candidates, subscriptions, selection, diagnostics, and recovery.

### Safety

- Unix Socket Mihomo controller with secret-protected loopback fallback.
- Strict failure classification, switch-rate limiting, cooldowns, and retry deduplication.
- Path traversal and symlink rejection for profile and enhancement files.
- Atomic state/config writes, runtime validation, configuration rollback, and private permissions.
- Redacted JSON and logs with no telemetry.

[0.5.0]: https://github.com/TTXXCC789/ag-nodeguard/releases/tag/v0.5.0

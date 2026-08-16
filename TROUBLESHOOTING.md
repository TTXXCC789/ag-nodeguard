# Troubleshooting

Start with the built-in diagnostics:

```sh
"$HOME/Library/Application Support/AntiGravityNodeGuard/bin/ag-nodeguard" doctor
"$HOME/Library/Application Support/AntiGravityNodeGuard/bin/ag-nodeguard" status --json
```

Do not paste unredacted logs into a public issue. Subscription URLs, credentials, IP addresses, full paths containing a user name, conversation IDs, prompts, and response content must be removed.

## Dashboard says the daemon is offline

```sh
"$HOME/Library/Application Support/AntiGravityNodeGuard/bin/ag-nodeguard" start
```

If the binary is missing, open the app and choose **Install background service**. If start fails, run `doctor` and inspect only the local log:

```sh
"$HOME/Library/Application Support/AntiGravityNodeGuard/bin/ag-nodeguard" tail
```

## Anti Gravity local service is unavailable

NodeGuard cannot prove Agent state when the Anti Gravity language server is not running. Start Anti Gravity and run `doctor` again. A healthy `/healthz` confirms only the local process; real Agent availability still requires backend success evidence.

## No green candidates

Check:

```sh
ag-nodeguard subscriptions --json
ag-nodeguard candidates --json
```

Possible causes:

- All eligible nodes exceed the configured green threshold.
- The profile contains only `proxy-providers`, which v0.5.0 does not import.
- Nodes are in geographic/network cooldown.
- The subscription uses regions not listed in `allowed_regions`.
- Mihomo delay testing is temporarily unavailable.

Use `subscriptions --refresh --json` to rescan and transactionally rebuild the managed configuration. This command does not change Clash Verge's active profile.

## Preferred subscription shows Fallback

Fallback is expected when the preferred subscription has no green node outside cooldown. NodeGuard keeps the actual source visible. It returns to the preferred pool only when the Agent is idle or on the next eligible failure, avoiding interruption of a healthy task.

## Retry did not run

Background Retry intentionally stops when:

- Automatic switching is paused.
- The failure category is not a network/node failure.
- The conversation/task identity is ambiguous.
- The task/node retry key was already used inside its deduplication window.
- CDP or Connect-RPC is incompatible with the installed Anti Gravity version.
- The switch-rate limit has been reached.

Run `doctor` to verify Connect-RPC and CDP availability. NodeGuard does not fall back to mouse clicks or accessibility automation.

## Clash Verge update removed the managed group

The daemon compares subscription fingerprints every 10 seconds and rebuilds when the index, profile contents, or managed selector changes. Force a safe rebuild with:

```sh
ag-nodeguard subscriptions --refresh --json
```

If validation fails, the previous runtime and enhancements are restored. Report the failure category and version numbers, not the profile contents.

## UI data is stale

The app maintains `status --watch --json --interval 2s`. It reconnects after 1/2/4/8/15 seconds. If the daemon is healthy but the UI remains stale, quit only the UI and reopen it; the daemon continues running.

## Pause, resume, and removal

```sh
ag-nodeguard pause      # keep monitoring; disable switching and Retry
ag-nodeguard resume
ag-nodeguard uninstall  # remove daemon, managed enhancements, and runtime
```

Removing only the desktop UI leaves the daemon and managed route in place:

```sh
./macos-app/scripts/uninstall-ui.sh
```

## Compatibility errors

v0.5.0 is tested with Clash Verge Rev 2.5.x, Mihomo 1.19+ (1.x), and Anti Gravity 2.8.x. Internal interface changes are treated as incompatible until reviewed and tested. Include exact component versions in a bug report.

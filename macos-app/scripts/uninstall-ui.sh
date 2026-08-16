#!/bin/zsh
set -euo pipefail

target_app="/Applications/Anti Gravity NodeGuard.app"
launch_target="${HOME}/Library/LaunchAgents/com.local.ag-nodeguard-ui.plist"
domain="gui/$(/usr/bin/id -u)"

/bin/launchctl bootout "${domain}/com.local.ag-nodeguard-ui" >/dev/null 2>&1 || true
if [[ -f "${launch_target}" ]]; then
  /bin/rm "${launch_target}"
fi
if [[ -e "${target_app}" ]]; then
  [[ "${target_app:h}" == "/Applications" && "${target_app:t}" == "Anti Gravity NodeGuard.app" ]]
  /bin/rm -rf "${target_app}"
fi
print "Removed NodeGuard UI only. The Go daemon and Clash routing are unchanged."

#!/bin/zsh
set -euo pipefail

script_dir=${0:A:h}
project_dir=${script_dir:h:h}
source_app="${project_dir}/dist/Anti Gravity NodeGuard.app"
target_app="/Applications/Anti Gravity NodeGuard.app"
launch_source="${script_dir:h}/Resources/com.local.ag-nodeguard-ui.plist"
launch_target="${HOME}/Library/LaunchAgents/com.local.ag-nodeguard-ui.plist"
domain="gui/$(/usr/bin/id -u)"

[[ -d "${source_app}" ]] || { print -u2 "Build the app first: ${script_dir}/build-app.sh"; exit 1; }
/bin/mkdir -p "${HOME}/Library/LaunchAgents"
if [[ -e "${target_app}" ]]; then
  [[ "${target_app:h}" == "/Applications" && "${target_app:t}" == "Anti Gravity NodeGuard.app" ]]
  /bin/rm -rf "${target_app}"
fi
/usr/bin/ditto "${source_app}" "${target_app}"
/bin/cp "${launch_source}" "${launch_target}"
/bin/chmod 600 "${launch_target}"
/bin/launchctl bootout "${domain}/com.local.ag-nodeguard-ui" >/dev/null 2>&1 || true
/bin/launchctl bootstrap "${domain}" "${launch_target}"
print "Installed ${target_app}"

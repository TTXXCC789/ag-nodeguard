#!/bin/zsh
set -euo pipefail

script_dir=${0:A:h}
app_dir=${script_dir:h}
project_dir=${app_dir:h}
dist_dir="${project_dir}/dist"
app_bundle="${dist_dir}/Anti Gravity NodeGuard.app"
contents="${app_bundle}/Contents"

if [[ -n "${GO_BIN:-}" ]]; then
  go_command="${GO_BIN}"
elif command -v go >/dev/null 2>&1; then
  go_command=$(command -v go)
else
  go_command=""
fi

if [[ -n "${go_command}" ]]; then
  "${go_command}" build -trimpath -o "${project_dir}/bin/ag-nodeguard" ./cmd/ag-nodeguard
elif [[ ! -x "${project_dir}/bin/ag-nodeguard" ]]; then
  print -u2 "Go is unavailable and no prebuilt ag-nodeguard binary exists."
  exit 1
fi

/usr/bin/xcrun swift build --package-path "${app_dir}" -c release
swift_bin_dir=$(/usr/bin/xcrun swift build --package-path "${app_dir}" -c release --show-bin-path)

/bin/mkdir -p "${dist_dir}"
if [[ -e "${app_bundle}" ]]; then
  [[ "${app_bundle:h}" == "${dist_dir}" && "${app_bundle:t}" == "Anti Gravity NodeGuard.app" ]]
  /bin/rm -rf "${app_bundle}"
fi
/bin/mkdir -p "${contents}/MacOS" "${contents}/Resources"
/bin/cp "${swift_bin_dir}/AntiGravityNodeGuardUI" "${contents}/MacOS/AntiGravityNodeGuardUI"
/bin/cp "${project_dir}/bin/ag-nodeguard" "${contents}/Resources/ag-nodeguard"
/bin/cp "${app_dir}/Resources/Info.plist" "${contents}/Info.plist"
/bin/chmod 755 "${contents}/MacOS/AntiGravityNodeGuardUI" "${contents}/Resources/ag-nodeguard"
/usr/bin/strip -S "${contents}/MacOS/AntiGravityNodeGuardUI"

icon_work=$(/usr/bin/mktemp -d)
trap '/bin/rm -rf "$icon_work"' EXIT
/usr/bin/qlmanage -t -s 1024 -o "${icon_work}" "${app_dir}/Resources/AppIcon.svg" >/dev/null 2>&1
icon_png="${icon_work}/AppIcon.svg.png"
iconset="${icon_work}/AppIcon.iconset"
/bin/mkdir "${iconset}"
for spec in '16 icon_16x16.png' '32 icon_16x16@2x.png' '32 icon_32x32.png' '64 icon_32x32@2x.png' '128 icon_128x128.png' '256 icon_128x128@2x.png' '256 icon_256x256.png' '512 icon_256x256@2x.png' '512 icon_512x512.png' '1024 icon_512x512@2x.png'; do
  size=${spec%% *}
  name=${spec#* }
  /usr/bin/sips -z "${size}" "${size}" "${icon_png}" --out "${iconset}/${name}" >/dev/null
done
/usr/bin/iconutil -c icns "${iconset}" -o "${contents}/Resources/AppIcon.icns"

/usr/bin/plutil -lint "${contents}/Info.plist" >/dev/null
/usr/bin/codesign --force --deep --sign - "${app_bundle}"
print "Built ${app_bundle}"

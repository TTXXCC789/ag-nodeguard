#!/bin/zsh
set -euo pipefail

script_dir=${0:A:h}
app_dir=${script_dir:h}
test_binary="${app_dir}/.build/nodeguard-self-test"
/bin/mkdir -p "${app_dir}/.build"

/usr/bin/xcrun swiftc \
  "${app_dir}/Sources/AntiGravityNodeGuardUI/Models.swift" \
  "${app_dir}/Sources/AntiGravityNodeGuardUI/CLIClient.swift" \
  "${app_dir}/Tests/SelfTest.swift" \
  -o "${test_binary}"
"${test_binary}"

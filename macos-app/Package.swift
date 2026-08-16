// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "AntiGravityNodeGuardUI",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "AntiGravityNodeGuardUI", targets: ["AntiGravityNodeGuardUI"])
    ],
    targets: [
        .executableTarget(name: "AntiGravityNodeGuardUI")
    ]
)

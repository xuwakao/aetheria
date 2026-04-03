// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "AetheriaDisplay",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(
            name: "AetheriaDisplay",
            path: "Sources",
            linkerSettings: [
                .linkedFramework("Metal"),
                .linkedFramework("MetalKit"),
                .linkedFramework("AppKit"),
            ]
        ),
    ]
)

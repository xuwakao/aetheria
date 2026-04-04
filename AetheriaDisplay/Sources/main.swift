// AetheriaDisplay — macOS display renderer for Aetheria VM.
//
// Reads framebuffer data from shared memory (written by crosvm virtio-gpu)
// and renders it via Metal in a native macOS window.
//
// IPC:
//   - Shared memory: /tmp/aetheria-display.shm (header + XRGB8888 framebuffer)
//   - Control socket: /tmp/aetheria-display.sock (frame ready signal + input events)

import AppKit

// --test-pattern: Generate animated test frames (no VM needed).
// --test-pattern runs the frame generator in background, display app renders them.
if CommandLine.arguments.contains("--test-pattern") {
    print("AetheriaDisplay test mode: generating 1024x768 test pattern at 60fps")
    let gen = TestPatternGenerator()
    DispatchQueue.global().async { gen.run() }
    // Small delay to let the generator create shared memory before app connects.
    Thread.sleep(forTimeInterval: 0.5)
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()

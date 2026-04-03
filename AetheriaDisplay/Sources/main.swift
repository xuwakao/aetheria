// AetheriaDisplay — macOS display renderer for Aetheria VM.
//
// Reads framebuffer data from shared memory (written by crosvm virtio-gpu)
// and renders it via Metal in a native macOS window.
//
// IPC:
//   - Shared memory: /tmp/aetheria-display.shm (header + XRGB8888 framebuffer)
//   - Control socket: /tmp/aetheria-display.sock (frame ready signal + input events)

import AppKit

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()

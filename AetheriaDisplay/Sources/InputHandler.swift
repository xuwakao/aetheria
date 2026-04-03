import AppKit

/// Captures keyboard and mouse events from the window and sends them
/// to crosvm via the control socket.
class InputHandler {
    static let shared = InputHandler()

    var controlSocket: ControlSocket?

    /// macOS keyCode → Linux scancode mapping (common keys).
    /// macOS uses virtual keycodes (CGKeyCode), Linux uses AT scancodes.
    private static let macToLinuxScancode: [UInt16: UInt32] = [
        // Letters
        0x00: 30,  // A
        0x01: 31,  // S
        0x02: 32,  // D
        0x03: 33,  // F
        0x04: 35,  // H
        0x05: 34,  // G
        0x06: 44,  // Z
        0x07: 45,  // X
        0x08: 46,  // C
        0x09: 47,  // V
        0x0B: 48,  // B
        0x0C: 16,  // Q
        0x0D: 17,  // W
        0x0E: 18,  // E
        0x0F: 19,  // R
        0x10: 21,  // Y
        0x11: 20,  // T
        0x12: 2,   // 1
        0x13: 3,   // 2
        0x14: 4,   // 3
        0x15: 5,   // 4
        0x16: 7,   // 6
        0x17: 6,   // 5
        0x18: 13,  // =
        0x19: 10,  // 9
        0x1A: 8,   // 7
        0x1B: 12,  // -
        0x1C: 9,   // 8
        0x1D: 11,  // 0
        0x1E: 27,  // ]
        0x1F: 24,  // O
        0x20: 22,  // U
        0x21: 26,  // [
        0x22: 23,  // I
        0x23: 25,  // P
        0x25: 38,  // L
        0x26: 36,  // J
        0x27: 40,  // '
        0x28: 37,  // K
        0x29: 39,  // ;
        0x2A: 43,  // backslash
        0x2B: 51,  // ,
        0x2C: 53,  // /
        0x2D: 49,  // N
        0x2E: 50,  // M
        0x2F: 52,  // .
        // Special keys
        0x24: 28,  // Return
        0x30: 15,  // Tab
        0x31: 57,  // Space
        0x33: 14,  // Backspace
        0x35: 1,   // Escape
        0x37: 125, // Left Command (→ Linux Super)
        0x38: 42,  // Left Shift
        0x39: 58,  // Caps Lock
        0x3A: 56,  // Left Alt
        0x3B: 29,  // Left Control
        0x3C: 54,  // Right Shift
        0x3D: 100, // Right Alt
        0x3E: 97,  // Right Control
        // Arrow keys
        0x7B: 105, // Left
        0x7C: 106, // Right
        0x7D: 108, // Down
        0x7E: 103, // Up
        // Function keys
        0x7A: 59,  // F1
        0x78: 60,  // F2
        0x63: 61,  // F3
        0x76: 62,  // F4
        0x60: 63,  // F5
        0x61: 64,  // F6
        0x62: 65,  // F7
        0x64: 66,  // F8
        0x65: 67,  // F9
        0x6D: 68,  // F10
        0x67: 87,  // F11
        0x6F: 88,  // F12
        // Other
        0x32: 41,  // `
        0x75: 111, // Delete (forward)
        0x73: 102, // Home
        0x77: 107, // End
        0x74: 104, // Page Up
        0x79: 109, // Page Down
    ]

    func handleKeyEvent(_ event: NSEvent) {
        guard let scancode = InputHandler.macToLinuxScancode[event.keyCode] else { return }
        let pressed = event.type == .keyDown
        controlSocket?.sendKeyEvent(scancode: scancode, pressed: pressed)
    }

    func handleMouseMove(_ event: NSEvent) {
        guard let window = event.window else { return }
        let point = event.locationInWindow
        let height = window.contentView?.bounds.height ?? 0
        // Convert from AppKit coordinates (origin bottom-left) to screen coordinates (origin top-left).
        let x = UInt32(max(0, point.x))
        let y = UInt32(max(0, height - point.y))
        let buttons: UInt8 = UInt8(NSEvent.pressedMouseButtons & 0xFF)
        controlSocket?.sendMouseMove(x: x, y: y, buttons: buttons)
    }

    func handleMouseClick(_ event: NSEvent) {
        guard let window = event.window else { return }
        let point = event.locationInWindow
        let height = window.contentView?.bounds.height ?? 0
        let x = UInt32(max(0, point.x))
        let y = UInt32(max(0, height - point.y))
        let button: UInt8 = event.buttonNumber == 0 ? 0 : 1 // 0=left, 1=right
        let pressed = event.type == .leftMouseDown || event.type == .rightMouseDown
        controlSocket?.sendMouseClick(x: x, y: y, button: button, pressed: pressed)
    }
}

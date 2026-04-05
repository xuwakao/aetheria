import Foundation

/// Connects to crosvm's control socket for frame ready notifications and input events.
///
/// Protocol:
///   crosvm → app: 'F' (frame ready)
///   crosvm → app: 'R' + u32le(width) + u32le(height) (resize)
///   app → crosvm: 'K' + u32le(scancode) + u8(pressed) (key)
///   app → crosvm: 'M' + u32le(x) + u32le(y) + u8(buttons) (mouse move)
///   app → crosvm: 'C' + u32le(x) + u32le(y) + u8(button) + u8(pressed) (click)
class ControlSocket {
    static let socketPath = "/tmp/aetheria-display.sock"

    private var fd: Int32 = -1
    private var readSource: DispatchSourceRead?

    var onFrameReady: (() -> Void)?
    var onResize: ((UInt32, UInt32) -> Void)?

    var isConnected: Bool { fd >= 0 }

    func connect() -> Bool {
        fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return false }

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let path = ControlSocket.socketPath
        let maxLen = MemoryLayout.size(ofValue: addr.sun_path) - 1
        _ = path.withCString { cstr in
            memcpy(&addr.sun_path, cstr, min(path.utf8.count, maxLen))
        }

        let addrLen = socklen_t(MemoryLayout<sockaddr_un>.size)
        let result = withUnsafePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                Darwin.connect(fd, sockPtr, addrLen)
            }
        }

        if result != 0 {
            close(fd)
            fd = -1
            return false
        }

        // Non-blocking so input writes never block the main thread.
        // crosvm may not read input messages, so the send buffer can fill up.
        var flags = fcntl(fd, F_GETFL)
        fcntl(fd, F_SETFL, flags | O_NONBLOCK)

        return true
    }

    func startListening() {
        if !isConnected {
            guard connect() else {
                print("[display] Control socket not available, retrying...")
                DispatchQueue.global().asyncAfter(deadline: .now() + 1.0) { [weak self] in
                    self?.startListening()
                }
                return
            }
        }

        let source = DispatchSource.makeReadSource(fileDescriptor: fd, queue: DispatchQueue(label: "display.socket"))
        source.setEventHandler { [weak self] in
            self?.handleRead()
        }
        source.setCancelHandler { [weak self] in
            if let fd = self?.fd, fd >= 0 {
                close(fd)
                self?.fd = -1
            }
        }
        source.resume()
        readSource = source
        print("[display] Control socket connected")
    }

    private func handleRead() {
        var buf = [UInt8](repeating: 0, count: 16)
        let n = read(fd, &buf, buf.count)
        guard n > 0 else {
            print("[display] Control socket disconnected")
            readSource?.cancel()
            readSource = nil
            return
        }

        var offset = 0
        while offset < n {
            switch buf[offset] {
            case 0x46: // 'F' — frame ready
                onFrameReady?()
                offset += 1
            case 0x52: // 'R' — resize
                if offset + 9 <= n {
                    let w = decodeU32(buf, at: offset + 1)
                    let h = decodeU32(buf, at: offset + 5)
                    onResize?(w, h)
                    offset += 9
                } else {
                    offset = n // incomplete, discard
                }
            default:
                offset += 1 // skip unknown
            }
        }
    }

    // Encode UInt32 as little-endian bytes (avoids unaligned memory access).
    private func encodeU32(_ value: UInt32) -> [UInt8] {
        return [UInt8(value & 0xFF), UInt8((value >> 8) & 0xFF),
                UInt8((value >> 16) & 0xFF), UInt8((value >> 24) & 0xFF)]
    }

    private func decodeU32(_ bytes: [UInt8], at offset: Int) -> UInt32 {
        return UInt32(bytes[offset]) | UInt32(bytes[offset+1]) << 8 |
               UInt32(bytes[offset+2]) << 16 | UInt32(bytes[offset+3]) << 24
    }

    /// Send key event to crosvm.
    func sendKeyEvent(scancode: UInt32, pressed: Bool) {
        guard fd >= 0 else { return }
        var msg: [UInt8] = [0x4B] // 'K'
        msg.append(contentsOf: encodeU32(scancode))
        msg.append(pressed ? 1 : 0)
        write(fd, msg, msg.count)
    }

    /// Send mouse move to crosvm.
    func sendMouseMove(x: UInt32, y: UInt32, buttons: UInt8) {
        guard fd >= 0 else { return }
        var msg: [UInt8] = [0x4D] // 'M'
        msg.append(contentsOf: encodeU32(x))
        msg.append(contentsOf: encodeU32(y))
        msg.append(buttons)
        write(fd, msg, msg.count)
    }

    /// Send mouse click to crosvm.
    func sendMouseClick(x: UInt32, y: UInt32, button: UInt8, pressed: Bool) {
        guard fd >= 0 else { return }
        var msg: [UInt8] = [0x43] // 'C'
        msg.append(contentsOf: encodeU32(x))
        msg.append(contentsOf: encodeU32(y))
        msg.append(button)
        msg.append(pressed ? 1 : 0)
        write(fd, msg, msg.count)
    }

    deinit {
        readSource?.cancel()
        if fd >= 0 { close(fd) }
    }
}

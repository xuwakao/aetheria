import Foundation

/// Generates test pattern frames to shared memory for verifying AetheriaDisplay
/// rendering without a running VM.
///
/// Usage: AetheriaDisplay --test-pattern
class TestPatternGenerator {
    static let shmPath = "/tmp/aetheria-display.shm"
    static let socketPath = "/tmp/aetheria-display.sock"
    static let headerSize = 4096
    static let width: UInt32 = 1024
    static let height: UInt32 = 768
    static let stride: UInt32 = 1024 * 4
    static let magic: UInt32 = 0x4845_5441

    private var pointer: UnsafeMutableRawPointer?
    private var mappedSize: Int = 0
    private var listener: Int32 = -1
    private var client: Int32 = -1

    func run() {
        print("[test] Creating shared memory: \(TestPatternGenerator.width)x\(TestPatternGenerator.height)")

        // Create shared memory file.
        let fd = open(TestPatternGenerator.shmPath, O_RDWR | O_CREAT | O_TRUNC, 0o644)
        guard fd >= 0 else {
            print("[test] Failed to create shm file")
            return
        }

        let fbSize = Int(TestPatternGenerator.width) * Int(TestPatternGenerator.height) * 4
        let totalSize = TestPatternGenerator.headerSize + fbSize * 2
        ftruncate(fd, off_t(totalSize))

        let ptr = mmap(nil, totalSize, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0)
        close(fd)
        guard ptr != MAP_FAILED else {
            print("[test] mmap failed")
            return
        }

        pointer = ptr
        mappedSize = totalSize

        // Write header (magic last).
        ptr!.storeBytes(of: UInt32(1), toByteOffset: 4, as: UInt32.self) // version
        ptr!.storeBytes(of: TestPatternGenerator.width, toByteOffset: 8, as: UInt32.self)
        ptr!.storeBytes(of: TestPatternGenerator.height, toByteOffset: 12, as: UInt32.self)
        ptr!.storeBytes(of: TestPatternGenerator.stride, toByteOffset: 16, as: UInt32.self)
        ptr!.storeBytes(of: UInt32(0x3432_5258), toByteOffset: 20, as: UInt32.self) // XRGB8888
        ptr!.storeBytes(of: UInt32(0), toByteOffset: 24, as: UInt32.self) // frame_seq
        ptr!.storeBytes(of: UInt32(0), toByteOffset: 28, as: UInt32.self) // active_buffer
        ptr!.storeBytes(of: TestPatternGenerator.magic, toByteOffset: 0, as: UInt32.self)

        // Start socket listener.
        setupSocket()

        // Generate animated frames.
        print("[test] Generating test pattern frames... Launch AetheriaDisplay to see them.")
        print("[test] Press Ctrl+C to stop.")

        var frameNum: UInt32 = 0
        while true {
            let backIdx = 1 - (ptr!.load(fromByteOffset: 28, as: UInt32.self) & 1)
            let backOffset = TestPatternGenerator.headerSize + Int(backIdx) * fbSize
            let backPtr = ptr!.advanced(by: backOffset).assumingMemoryBound(to: UInt32.self)

            // Generate color cycling pattern.
            let w = Int(TestPatternGenerator.width)
            let h = Int(TestPatternGenerator.height)
            for y in 0..<h {
                for x in 0..<w {
                    let r = UInt32((x + Int(frameNum) * 2) & 0xFF)
                    let g = UInt32((y + Int(frameNum)) & 0xFF)
                    let b = UInt32(((x + y) / 2 + Int(frameNum) * 3) & 0xFF)
                    // XRGB8888 in memory: [B, G, R, X] (little-endian)
                    backPtr[y * w + x] = (r << 16) | (g << 8) | b | 0xFF000000
                }
            }

            // Swap buffers.
            let oldActive = ptr!.load(fromByteOffset: 28, as: UInt32.self) & 1
            ptr!.storeBytes(of: 1 - oldActive, toByteOffset: 28, as: UInt32.self)
            frameNum &+= 1
            ptr!.storeBytes(of: frameNum, toByteOffset: 24, as: UInt32.self)

            // Signal display app.
            if client >= 0 {
                var f: UInt8 = 0x46 // 'F'
                if write(client, &f, 1) <= 0 {
                    close(client)
                    client = -1
                }
            }

            // Accept new connections.
            if client < 0 {
                var addr = sockaddr_un()
                var addrLen = socklen_t(MemoryLayout<sockaddr_un>.size)
                let c = withUnsafeMutablePointer(to: &addr) { ptr in
                    ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                        accept(listener, sockPtr, &addrLen)
                    }
                }
                if c >= 0 {
                    client = c
                    print("[test] Display app connected")
                }
            }

            usleep(16667) // ~60fps
        }
    }

    private func setupSocket() {
        unlink(TestPatternGenerator.socketPath)
        listener = socket(AF_UNIX, SOCK_STREAM, 0)
        guard listener >= 0 else { return }

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let path = TestPatternGenerator.socketPath
        let maxLen = MemoryLayout.size(ofValue: addr.sun_path) - 1
        _ = path.withCString { cstr in
            memcpy(&addr.sun_path, cstr, min(path.utf8.count, maxLen))
        }

        withUnsafePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                bind(listener, sockPtr, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        listen(listener, 1)

        // Non-blocking accept.
        var flags = fcntl(listener, F_GETFL)
        flags |= O_NONBLOCK
        fcntl(listener, F_SETFL, flags)
    }

    deinit {
        if let ptr = pointer { munmap(ptr, mappedSize) }
        if listener >= 0 { close(listener) }
        if client >= 0 { close(client) }
        unlink(TestPatternGenerator.shmPath)
        unlink(TestPatternGenerator.socketPath)
    }
}

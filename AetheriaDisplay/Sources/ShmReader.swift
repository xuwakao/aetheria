import Foundation

/// Reads the shared memory framebuffer written by crosvm's SharedMemory display backend.
///
/// Layout (double-buffered):
///   Offset 0:                ShmHeader (padded to 4096)
///   Offset 4096:             Buffer 0 (width * height * 4, XRGB8888)
///   Offset 4096 + fb_size:   Buffer 1 (width * height * 4, XRGB8888)
class ShmReader {
    static let shmPath = "/tmp/aetheria-display.shm"
    static let headerSize = 4096
    static let magic: UInt32 = 0x4845_5441 // "AETH" little-endian

    private var pointer: UnsafeMutableRawPointer?
    private var mappedSize: Int = 0

    var isValid: Bool { pointer != nil }

    var width: UInt32 {
        guard let ptr = pointer else { return 0 }
        return ptr.load(fromByteOffset: 8, as: UInt32.self)
    }

    var height: UInt32 {
        guard let ptr = pointer else { return 0 }
        return ptr.load(fromByteOffset: 12, as: UInt32.self)
    }

    var stride: UInt32 {
        guard let ptr = pointer else { return 0 }
        return ptr.load(fromByteOffset: 16, as: UInt32.self)
    }

    var frameSeq: UInt32 {
        guard let ptr = pointer else { return 0 }
        return ptr.load(fromByteOffset: 24, as: UInt32.self)
    }

    /// Active (front) buffer index: 0 or 1. Swift reads from this buffer.
    var activeBuffer: UInt32 {
        guard let ptr = pointer else { return 0 }
        return ptr.load(fromByteOffset: 28, as: UInt32.self) & 1
    }

    var singleBufferSize: Int {
        return Int(width) * Int(height) * 4
    }

    /// Pointer to the FRONT buffer (the one Swift should read from).
    /// Double-buffered: buffer 0 at offset 4096, buffer 1 at offset 4096 + fb_size.
    var framebufferPointer: UnsafeRawPointer? {
        guard let ptr = pointer else { return nil }
        let bufferOffset = ShmReader.headerSize + Int(activeBuffer) * singleBufferSize
        return UnsafeRawPointer(ptr.advanced(by: bufferOffset))
    }

    var framebufferSize: Int {
        return singleBufferSize
    }

    /// Pointer to a specific buffer slot (0 or 1).
    func bufferPointer(index: Int) -> UnsafeRawPointer? {
        guard let ptr = pointer, index == 0 || index == 1 else { return nil }
        let offset = ShmReader.headerSize + index * singleBufferSize
        guard offset + singleBufferSize <= mappedSize else { return nil }
        return UnsafeRawPointer(ptr.advanced(by: offset))
    }

    init() {
        let fd = open(ShmReader.shmPath, O_RDONLY)
        guard fd >= 0 else { return }
        defer { close(fd) }

        // Get file size.
        var stat = stat()
        guard fstat(fd, &stat) == 0, stat.st_size > Int64(ShmReader.headerSize) else { return }

        let size = Int(stat.st_size)
        let ptr = mmap(nil, size, PROT_READ, MAP_SHARED, fd, 0)
        guard ptr != MAP_FAILED else { return }

        // Verify magic.
        let magic = ptr!.load(fromByteOffset: 0, as: UInt32.self)
        guard magic == ShmReader.magic else {
            munmap(ptr, size)
            return
        }

        self.pointer = ptr
        self.mappedSize = size
    }

    deinit {
        if let ptr = pointer {
            munmap(ptr, mappedSize)
        }
    }
}

import Foundation

/// Reads the shared memory framebuffer written by crosvm's SharedMemory display backend.
///
/// Layout:
///   Offset 0:    ShmHeader (32 bytes, padded to 4096)
///   Offset 4096: XRGB8888 framebuffer data
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

    /// Pointer to the framebuffer data (after the 4096-byte header).
    var framebufferPointer: UnsafeMutableRawPointer? {
        guard let ptr = pointer else { return nil }
        return ptr.advanced(by: ShmReader.headerSize)
    }

    var framebufferSize: Int {
        return Int(width) * Int(height) * 4
    }

    init() {
        let fd = open(ShmReader.shmPath, O_RDONLY)
        guard fd >= 0 else { return }
        defer { close(fd) }

        // Get file size.
        var stat = stat()
        guard fstat(fd, &stat) == 0, stat.st_size > ShmReader.headerSize else { return }

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

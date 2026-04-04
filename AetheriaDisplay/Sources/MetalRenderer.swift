import Metal
import MetalKit

/// Renders the shared memory framebuffer to a Metal view.
///
/// On Apple Silicon (unified memory), uses MTLBuffer backed by the shared memory
/// mmap. The GPU reads directly from the shared pages — true zero-copy when the
/// framebuffer is page-aligned. Falls back to texture upload for non-aligned buffers.
class MetalRenderer: NSObject, MTKViewDelegate {
    private let device: MTLDevice
    private let commandQueue: MTLCommandQueue
    private let pipelineState: MTLRenderPipelineState
    private let shmReader: ShmReader

    // Double-buffered textures matching the shared memory layout.
    // Pre-created at initialization to avoid per-frame allocation.
    private var textures: [MTLTexture?] = [nil, nil]
    private var buffers: [MTLBuffer?] = [nil, nil]
    private var useZeroCopy = false
    private var lastFrameSeq: UInt32 = 0
    private var currentWidth: UInt32 = 0
    private var currentHeight: UInt32 = 0

    init?(device: MTLDevice, shmReader: ShmReader) {
        self.device = device
        self.shmReader = shmReader

        guard let queue = device.makeCommandQueue() else { return nil }
        self.commandQueue = queue

        let library: MTLLibrary
        do {
            let shaderSource = """
            #include <metal_stdlib>
            using namespace metal;

            struct VertexOut {
                float4 position [[position]];
                float2 texCoord;
            };

            vertex VertexOut vertexShader(uint vertexID [[vertex_id]]) {
                float2 positions[4] = {
                    float2(-1, -1), float2(1, -1),
                    float2(-1,  1), float2(1,  1)
                };
                float2 texCoords[4] = {
                    float2(0, 1), float2(1, 1),
                    float2(0, 0), float2(1, 0)
                };
                VertexOut out;
                out.position = float4(positions[vertexID], 0, 1);
                out.texCoord = texCoords[vertexID];
                return out;
            }

            fragment float4 fragmentShader(VertexOut in [[stage_in]],
                                           texture2d<float> tex [[texture(0)]]) {
                constexpr sampler s(mag_filter::nearest, min_filter::nearest);
                float4 color = tex.sample(s, in.texCoord);
                return float4(color.rgb, 1.0);
            }
            """
            library = try device.makeLibrary(source: shaderSource, options: nil)
        } catch {
            print("[display] Failed to compile shaders: \(error)")
            return nil
        }

        let descriptor = MTLRenderPipelineDescriptor()
        descriptor.vertexFunction = library.makeFunction(name: "vertexShader")
        descriptor.fragmentFunction = library.makeFunction(name: "fragmentShader")
        descriptor.colorAttachments[0].pixelFormat = .bgra8Unorm

        do {
            pipelineState = try device.makeRenderPipelineState(descriptor: descriptor)
        } catch {
            print("[display] Failed to create pipeline: \(error)")
            return nil
        }

        super.init()
    }

    func mtkView(_ view: MTKView, drawableSizeWillChange size: CGSize) {
        // Force texture recreation on next draw.
        currentWidth = 0
        currentHeight = 0
    }

    func draw(in view: MTKView) {
        let w = shmReader.width
        let h = shmReader.height
        guard w > 0, h > 0 else { return }

        let seq = shmReader.frameSeq
        let activeIdx = Int(shmReader.activeBuffer)

        // Re-render previous frame for window expose events.
        if seq == lastFrameSeq, textures[activeIdx] != nil {
            renderTexture(textures[activeIdx]!, in: view)
            return
        }
        lastFrameSeq = seq

        // Recreate textures when dimensions change.
        if w != currentWidth || h != currentHeight {
            setupTextures(width: w, height: h)
            currentWidth = w
            currentHeight = h
        }

        if useZeroCopy {
            // Zero-copy path: texture is backed by shared memory. Just render.
            if let tex = textures[activeIdx] {
                renderTexture(tex, in: view)
            }
        } else {
            // Fallback: upload pixels to texture.
            guard let fbPtr = shmReader.framebufferPointer,
                  let tex = textures[0] else { return }
            let stride = Int(shmReader.stride)
            tex.replace(
                region: MTLRegion(origin: MTLOrigin(x: 0, y: 0, z: 0),
                                  size: MTLSize(width: Int(w), height: Int(h), depth: 1)),
                mipmapLevel: 0, withBytes: fbPtr, bytesPerRow: stride)
            renderTexture(tex, in: view)
        }
    }

    /// Create textures for both double-buffer slots.
    /// Tries zero-copy (MTLBuffer from shared memory pointer) first.
    /// Falls back to regular texture if pointer is not page-aligned.
    private func setupTextures(width: UInt32, height: UInt32) {
        let stride = Int(width) * 4
        let bufferSize = Int(height) * stride
        let pageSize = Int(getpagesize())

        textures = [nil, nil]
        buffers = [nil, nil]
        useZeroCopy = false

        // Try zero-copy for both buffer slots.
        var zeroCopyOk = true
        for idx in 0..<2 {
            guard let ptr = shmReader.bufferPointer(index: idx) else {
                zeroCopyOk = false
                break
            }
            let ptrAddr = Int(bitPattern: ptr)
            // makeBuffer(bytesNoCopy:) requires page-aligned pointer and page-aligned length.
            let alignedSize = (bufferSize + pageSize - 1) & ~(pageSize - 1)
            if ptrAddr % pageSize != 0 {
                zeroCopyOk = false
                break
            }
            guard let buffer = device.makeBuffer(
                bytesNoCopy: UnsafeMutableRawPointer(mutating: ptr),
                length: alignedSize,
                options: .storageModeShared,
                deallocator: nil
            ) else {
                zeroCopyOk = false
                break
            }
            let texDesc = MTLTextureDescriptor.texture2DDescriptor(
                pixelFormat: .bgra8Unorm, width: Int(width), height: Int(height), mipmapped: false)
            texDesc.usage = [.shaderRead]
            texDesc.storageMode = .shared
            textures[idx] = buffer.makeTexture(descriptor: texDesc, offset: 0, bytesPerRow: stride)
            buffers[idx] = buffer
        }

        if zeroCopyOk && textures[0] != nil && textures[1] != nil {
            useZeroCopy = true
            print("[display] Zero-copy Metal rendering enabled (\(width)x\(height))")
        } else {
            // Fallback: single regular texture, upload each frame.
            let desc = MTLTextureDescriptor.texture2DDescriptor(
                pixelFormat: .bgra8Unorm, width: Int(width), height: Int(height), mipmapped: false)
            desc.usage = [.shaderRead]
            textures[0] = device.makeTexture(descriptor: desc)
            print("[display] Texture upload rendering (\(width)x\(height), not page-aligned)")
        }
    }

    private func renderTexture(_ tex: MTLTexture, in view: MTKView) {
        guard let drawable = view.currentDrawable,
              let descriptor = view.currentRenderPassDescriptor else { return }

        guard let commandBuffer = commandQueue.makeCommandBuffer(),
              let encoder = commandBuffer.makeRenderCommandEncoder(descriptor: descriptor) else { return }

        encoder.setRenderPipelineState(pipelineState)
        encoder.setFragmentTexture(tex, index: 0)
        encoder.drawPrimitives(type: .triangleStrip, vertexStart: 0, vertexCount: 4)
        encoder.endEncoding()

        commandBuffer.present(drawable)
        commandBuffer.commit()
    }
}

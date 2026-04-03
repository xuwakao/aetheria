import Metal
import MetalKit

/// Renders the shared memory framebuffer to a Metal view.
/// On each draw call, uploads XRGB8888 data from shared memory to a texture,
/// then draws a fullscreen textured quad.
class MetalRenderer: NSObject, MTKViewDelegate {
    private let device: MTLDevice
    private let commandQueue: MTLCommandQueue
    private let pipelineState: MTLRenderPipelineState
    private let shmReader: ShmReader
    private var texture: MTLTexture?
    private var lastFrameSeq: UInt32 = 0

    init?(device: MTLDevice, shmReader: ShmReader) {
        self.device = device
        self.shmReader = shmReader

        guard let queue = device.makeCommandQueue() else { return nil }
        self.commandQueue = queue

        // Create render pipeline with inline shaders.
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
                // Fullscreen triangle strip: 4 vertices → 2 triangles.
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
                // XRGB8888 maps to BGRA8Unorm — alpha channel is unused, set to 1.
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
        // Window resized — texture will be recreated on next draw if dimensions changed.
    }

    func draw(in view: MTKView) {
        let w = shmReader.width
        let h = shmReader.height
        guard w > 0, h > 0 else { return }

        // Check if frame has been updated.
        let seq = shmReader.frameSeq
        if seq == lastFrameSeq, texture != nil {
            // No new frame — re-render previous texture (for window expose events).
            renderTexture(in: view)
            return
        }
        lastFrameSeq = seq

        // Create or recreate texture if dimensions changed.
        if texture == nil || texture!.width != Int(w) || texture!.height != Int(h) {
            let desc = MTLTextureDescriptor.texture2DDescriptor(
                pixelFormat: .bgra8Unorm,
                width: Int(w),
                height: Int(h),
                mipmapped: false
            )
            desc.usage = [.shaderRead]
            texture = device.makeTexture(descriptor: desc)
        }

        guard let tex = texture, let fbPtr = shmReader.framebufferPointer else { return }

        // Upload framebuffer data to texture.
        tex.replace(
            region: MTLRegion(origin: MTLOrigin(x: 0, y: 0, z: 0),
                              size: MTLSize(width: Int(w), height: Int(h), depth: 1)),
            mipmapLevel: 0,
            withBytes: fbPtr,
            bytesPerRow: Int(shmReader.stride)
        )

        renderTexture(in: view)
    }

    private func renderTexture(in view: MTKView) {
        guard let drawable = view.currentDrawable,
              let descriptor = view.currentRenderPassDescriptor,
              let tex = texture else { return }

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

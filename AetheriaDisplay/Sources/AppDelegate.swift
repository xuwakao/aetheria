import AppKit
import MetalKit

class AppDelegate: NSObject, NSApplicationDelegate {
    var window: NSWindow!
    var metalView: MTKView!
    var renderer: MetalRenderer?
    var shmReader: ShmReader?
    var controlSocket: ControlSocket?

    func applicationDidFinishLaunching(_ notification: Notification) {
        // Connect to shared memory.
        shmReader = ShmReader()
        guard let shm = shmReader, shm.isValid else {
            print("[display] Failed to open shared memory at \(ShmReader.shmPath)")
            print("[display] Waiting for crosvm to start...")
            // Retry in background.
            retryConnect()
            return
        }

        setupWindow(width: Int(shm.width), height: Int(shm.height))
    }

    func retryConnect() {
        DispatchQueue.global().asyncAfter(deadline: .now() + 1.0) { [weak self] in
            let shm = ShmReader()
            if shm.isValid {
                DispatchQueue.main.async {
                    self?.shmReader = shm
                    self?.setupWindow(width: Int(shm.width), height: Int(shm.height))
                }
            } else {
                self?.retryConnect()
            }
        }
    }

    func setupWindow(width: Int, height: Int) {
        let contentRect = NSRect(x: 0, y: 0, width: width, height: height)

        window = NSWindow(
            contentRect: contentRect,
            styleMask: [.titled, .closable, .resizable, .miniaturizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Aetheria"
        window.center()

        // Create Metal view.
        guard let device = MTLCreateSystemDefaultDevice() else {
            print("[display] Metal not available")
            return
        }

        metalView = MTKView(frame: contentRect, device: device)
        metalView.colorPixelFormat = .bgra8Unorm
        metalView.preferredFramesPerSecond = 60

        // Set up renderer.
        renderer = MetalRenderer(device: device, shmReader: shmReader!)
        metalView.delegate = renderer

        window.contentView = metalView
        window.makeKeyAndOrderFront(nil)

        // Connect control socket for frame notifications.
        controlSocket = ControlSocket()
        controlSocket?.onFrameReady = { [weak self] in
            DispatchQueue.main.async {
                self?.metalView.setNeedsDisplay(self?.metalView.bounds ?? .zero)
            }
        }
        controlSocket?.startListening()

        // Set up input handling.
        InputHandler.shared.controlSocket = controlSocket
        NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .keyUp]) { event in
            InputHandler.shared.handleKeyEvent(event)
            return event
        }
        NSEvent.addLocalMonitorForEvents(matching: [.mouseMoved, .leftMouseDragged, .rightMouseDragged]) { event in
            InputHandler.shared.handleMouseMove(event)
            return event
        }
        NSEvent.addLocalMonitorForEvents(matching: [.leftMouseDown, .leftMouseUp, .rightMouseDown, .rightMouseUp]) { event in
            InputHandler.shared.handleMouseClick(event)
            return event
        }

        print("[display] Window ready: \(width)x\(height)")
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        return true
    }
}

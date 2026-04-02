// aetheria — macOS host CLI for managing the Aetheria VM.
//
// Architecture:
//   `aetheria run` acts as a daemon: starts crosvm, accepts agent vsock connection,
//   and listens on a local Unix socket for CLI commands.
//   `aetheria exec/ping/info/stop` connect to the daemon socket and relay to the agent.
//
// Commands:
//   aetheria run     — start VM, act as daemon (foreground)
//   aetheria exec    — execute command in VM
//   aetheria ping    — check agent health
//   aetheria info    — show VM information
//   aetheria stop    — shutdown VM

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	// vsock agent connection: crosvm maps vsock CID=2 port=1024 to this Unix socket.
	vsockDir  = "/tmp/aetheria-vsock-3"
	agentSock = vsockDir + "/port-1024"
	ptySock   = vsockDir + "/port-1025" // PTY byte stream

	// Daemon control socket: CLI commands connect here.
	daemonSock    = "/tmp/aetheria.sock"
	ptyDaemonSock = "/tmp/aetheria-pty.sock" // PTY forwarding socket for CLI
)

type Request struct {
	Method string      `json:"method"`
	Params interface{} `json:"params,omitempty"`
	ID     int         `json:"id"`
}

type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	ID     int             `json:"id"`
}

type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun()
	case "exec":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: aetheria exec <command>")
			os.Exit(1)
		}
		cmdExec(strings.Join(os.Args[2:], " "))
	case "ping":
		cmdPing()
	case "info":
		cmdInfo()
	case "stop":
		cmdStop()
	case "create":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: aetheria create <distro> [name] [--net=host|bridge|none]")
			os.Exit(1)
		}
		image := os.Args[2]
		name := image
		network := "bridge" // default
		for i := 3; i < len(os.Args); i++ {
			if strings.HasPrefix(os.Args[i], "--net=") {
				network = strings.TrimPrefix(os.Args[i], "--net=")
			} else if name == image {
				name = os.Args[i]
			}
		}
		cmdContainerCreate(image, name, network)
	case "start":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: aetheria start <name>")
			os.Exit(1)
		}
		cmdContainerAction("container.start", os.Args[2])
	case "shell":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: aetheria shell <name>")
			os.Exit(1)
		}
		cmdShell(os.Args[2])
	case "ls":
		cmdContainerList()
	case "rm":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: aetheria rm <name>")
			os.Exit(1)
		}
		cmdContainerAction("container.stop", os.Args[2])
		cmdContainerAction("container.remove", os.Args[2])
	case "images":
		cmdImageList()
	case "pull":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: aetheria pull <distro>")
			os.Exit(1)
		}
		cmdImagePull(os.Args[2])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `aetheria — lightweight Linux container runtime

Usage:
  aetheria run                 Start the VM (daemon mode)
  aetheria create <distro>     Create a container (alpine, ubuntu, debian)
  aetheria start <name>        Start a container
  aetheria shell <name>        Open a shell in a running container
  aetheria exec <command>      Execute a command in the VM
  aetheria ls                  List containers
  aetheria rm <name>           Stop and remove a container
  aetheria pull <distro>       Download a distro rootfs image
  aetheria images              List available/cached images
  aetheria ping                Check if VM agent is running
  aetheria info                Show VM information
  aetheria stop                Shutdown the VM`)
}

// ============================================================================
// Daemon (aetheria run)
// ============================================================================

// daemon holds the agent connection and multiplexes CLI requests.
type daemon struct {
	agentConn   net.Conn
	agentReader *bufio.Reader
	mu          sync.Mutex // serializes requests to agent
	idCounter   int
}

func (d *daemon) sendToAgent(req Request) (*Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.agentConn == nil {
		return nil, fmt.Errorf("agent not connected")
	}

	d.idCounter++
	req.ID = d.idCounter

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %v", err)
	}
	data = append(data, '\n')
	if _, err := d.agentConn.Write(data); err != nil {
		return nil, fmt.Errorf("agent write: %v", err)
	}

	// Read response (agent sends one line of JSON per request)
	d.agentConn.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := d.agentReader.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("agent read: %v", err)
	}

	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("agent response parse: %v", err)
	}
	return &resp, nil
}

func (d *daemon) handleCLIConn(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Read one request from CLI client
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}

	var req Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		writeJSONResponse(conn, Response{Error: "invalid request"})
		return
	}

	// Forward to agent
	resp, err := d.sendToAgent(req)
	if err != nil {
		writeJSONResponse(conn, Response{Error: err.Error(), ID: req.ID})
		return
	}

	writeJSONResponse(conn, *resp)
}

// writeJSONResponse marshals and sends a response to a connection.
func writeJSONResponse(conn net.Conn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		conn.Write([]byte("{\"error\":\"internal marshal error\"}\n"))
		return
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		// CLI disconnected mid-response — not actionable, just log.
		fmt.Fprintf(os.Stderr, "write response: %v\n", err)
	}
}

func cmdRun() {
	crosvmBin := os.Getenv("AETHERIA_CROSVM")
	kernel := os.Getenv("AETHERIA_KERNEL")
	rootfs := os.Getenv("AETHERIA_ROOTFS")
	initrd := os.Getenv("AETHERIA_INITRD")

	if crosvmBin == "" || kernel == "" || rootfs == "" {
		fmt.Fprintln(os.Stderr, "Set environment variables:")
		fmt.Fprintln(os.Stderr, "  AETHERIA_CROSVM  — path to crosvm binary")
		fmt.Fprintln(os.Stderr, "  AETHERIA_KERNEL  — path to kernel Image")
		fmt.Fprintln(os.Stderr, "  AETHERIA_ROOTFS  — path to rootfs ext4 image")
		fmt.Fprintln(os.Stderr, "  AETHERIA_INITRD  — path to initramfs (optional)")
		os.Exit(1)
	}

	// Clean up sockets
	os.MkdirAll(vsockDir, 0755)
	os.Remove(agentSock)
	os.Remove(ptySock)
	os.Remove(daemonSock)
	os.Remove(ptyDaemonSock)

	// 1. Listen for agent vsock connection (JSON-RPC)
	agentListener, err := net.Listen("unix", agentSock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent listen: %v\n", err)
		os.Exit(1)
	}
	defer agentListener.Close()

	// 1b. Listen for agent PTY stream connections
	ptyListener, err := net.Listen("unix", ptySock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pty listen: %v\n", err)
		os.Exit(1)
	}
	defer ptyListener.Close()

	// 1c. Listen for CLI PTY forwarding connections
	ptyDaemonListener, err := net.Listen("unix", ptyDaemonSock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pty daemon listen: %v\n", err)
		os.Exit(1)
	}
	defer ptyDaemonListener.Close()
	defer os.Remove(ptyDaemonSock)

	// Bridge PTY connections: agent → ptyDaemon → CLI
	go bridgePTYStreams(ptyListener, ptyDaemonListener)

	// 2. Listen for CLI commands
	cliListener, err := net.Listen("unix", daemonSock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli listen: %v\n", err)
		os.Exit(1)
	}
	defer cliListener.Close()
	defer os.Remove(daemonSock)

	// 3. Start crosvm
	args := []string{
		"run",
		"--mem", "512",
		"--cpus", "2",
		"--block", rootfs,
		"--serial", "type=stdout,hardware=serial,num=1",
		"-p", "root=/dev/vda rw console=ttyS0 earlycon=uart8250,mmio,0x3f8 loglevel=4",
	}
	if initrd != "" {
		args = append(args, "--initrd", initrd)
	}
	args = append(args, kernel)

	cmd := exec.Command("sudo", append([]string{"-n", crosvmBin}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Starting VM...")
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "crosvm start: %v\n", err)
		os.Exit(1)
	}

	// Handle Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutting down...")
		cmd.Process.Signal(syscall.SIGTERM)
		os.Remove(daemonSock)
	}()

	// 4. Wait for agent to connect
	d := &daemon{}

	// Accept agent connections continuously (handles reconnection).
	go func() {
		for {
			fmt.Print("Waiting for agent...")
			conn, err := agentListener.Accept()
			if err != nil {
				return // listener closed (daemon shutting down)
			}
			fmt.Println(" connected!")

			d.mu.Lock()
			if d.agentConn != nil {
				d.agentConn.Close() // close stale connection
			}
			d.agentConn = conn
			d.agentReader = bufio.NewReaderSize(conn, 1024*1024)
			d.mu.Unlock()

			// Verify with ping
			resp, err := d.sendToAgent(Request{Method: "ping"})
			if err != nil {
				fmt.Fprintf(os.Stderr, "agent ping failed: %v\n", err)
				continue
			}
			fmt.Printf("Agent ready: %s\n", string(resp.Result))
			fmt.Println("VM running. Use 'aetheria exec <cmd>' in another terminal.")
		}
	}()

	// Wait for first agent connection (with timeout)
	deadline := time.After(60 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		d.mu.Lock()
		connected := d.agentConn != nil
		d.mu.Unlock()
		if connected {
			break
		}
		select {
		case <-deadline:
			fmt.Println("\nAgent did not connect within 60 seconds")
			cmd.Process.Kill()
			os.Exit(1)
		case <-ticker.C:
			// poll
		}
	}

	// Accept CLI connections and relay to agent
	go func() {
		for {
			conn, err := cliListener.Accept()
			if err != nil {
				return // listener closed
			}
			go d.handleCLIConn(conn)
		}
	}()

	// Wait for crosvm to exit
	cmd.Wait()
	fmt.Println("VM stopped.")
}

// ============================================================================
// CLI commands (connect to daemon)
// ============================================================================

func sendToDaemon(req Request) (*Response, error) {
	conn, err := net.DialTimeout("unix", daemonSock, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to daemon (is VM running?): %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %v", err)
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("send to daemon: %v", err)
	}

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("daemon read: %v", err)
		}
		return nil, fmt.Errorf("no response from daemon")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parse daemon response: %v", err)
	}
	return &resp, nil
}

func cmdPing() {
	resp, err := sendToDaemon(Request{Method: "ping"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Println(strings.Trim(string(resp.Result), `"`))
}

func cmdInfo() {
	resp, err := sendToDaemon(Request{Method: "info"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
	var info map[string]interface{}
	if err := json.Unmarshal(resp.Result, &info); err != nil {
		fmt.Fprintf(os.Stderr, "parse info: %v\n", err)
		os.Exit(1)
	}
	for k, v := range info {
		fmt.Printf("%-12s %v\n", k+":", v)
	}
}

func cmdExec(command string) {
	resp, err := sendToDaemon(Request{
		Method: "exec",
		Params: map[string]string{"cmd": command},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}

	var result ExecResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		fmt.Fprintf(os.Stderr, "parse exec result: %v\n", err)
		os.Exit(1)
	}

	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	os.Exit(result.ExitCode)
}

func cmdStop() {
	// Send poweroff command — agent may not respond before VM dies
	conn, err := net.DialTimeout("unix", daemonSock, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	data, err := json.Marshal(Request{Method: "exec", Params: map[string]string{"cmd": "poweroff"}})
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
		os.Exit(1)
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "send shutdown: %v\n", err)
		os.Exit(1)
	}

	// Try to read response but don't fail if VM shuts down
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	if n > 0 {
		fmt.Println("Shutdown signal sent")
	} else {
		fmt.Println("VM shutting down...")
	}
}

// ============================================================================
// Container commands
// ============================================================================

func cmdContainerCreate(image, name, network string) {
	// First pull the image, then create the container.
	fmt.Printf("Pulling %s...\n", image)
	resp, err := sendToDaemon(Request{
		Method: "image.pull",
		Params: map[string]string{"name": image},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pull: %v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "pull: %s\n", resp.Error)
		os.Exit(1)
	}

	fmt.Printf("Creating container %s (network=%s)...\n", name, network)
	resp, err = sendToDaemon(Request{
		Method: "container.create",
		Params: map[string]interface{}{"name": name, "image": image, "network": network},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create: %v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "create: %s\n", resp.Error)
		os.Exit(1)
	}

	// Auto-start.
	fmt.Printf("Starting %s...\n", name)
	resp, err = sendToDaemon(Request{
		Method: "container.start",
		Params: map[string]string{"name": name},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "start: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Printf("Container %s is running. Use: aetheria shell %s\n", name, name)
}

func cmdContainerAction(method, name string) {
	resp, err := sendToDaemon(Request{
		Method: method,
		Params: map[string]string{"name": name},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Println(strings.Trim(string(resp.Result), `"`))
}

// cmdShell opens an interactive terminal session in a container.
func cmdShell(name string) {
	// 1. Send container.shell RPC to start PTY on the agent side.
	resp, err := sendToDaemon(Request{
		Method: "container.shell",
		Params: map[string]interface{}{"name": name, "rows": 24, "cols": 80},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "shell: %v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "shell: %s\n", resp.Error)
		os.Exit(1)
	}

	// 2. Connect to the daemon's PTY forwarding socket.
	// Retry: the agent needs time to dial the PTY vsock stream to the daemon.
	var conn net.Conn
	for i := 0; i < 20; i++ {
		time.Sleep(200 * time.Millisecond)
		conn, err = net.DialTimeout("unix", ptyDaemonSock, 2*time.Second)
		if err == nil {
			break
		}
	}
	if conn == nil {
		fmt.Fprintf(os.Stderr, "connect pty: timed out\n")
		os.Exit(1)
	}
	defer conn.Close()

	// Send container name to identify the session.
	conn.Write([]byte(name + "\n"))

	// 4. Set terminal to raw mode.
	oldState, err := makeRaw(os.Stdin.Fd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "raw mode: %v\n", err)
		os.Exit(1)
	}
	defer restoreTerminal(os.Stdin.Fd(), oldState)

	// 5. Bidirectional copy: stdin ↔ PTY stream ↔ stdout
	// When the shell exits, the agent closes the vsock connection.
	// io.Copy(os.Stdout, conn) returns on EOF. But io.Copy(conn, os.Stdin)
	// blocks forever on stdin.Read(). Fix: close conn after output ends
	// to unblock the stdin goroutine, then return immediately.
	go func() {
		io.Copy(conn, os.Stdin)
	}()
	io.Copy(os.Stdout, conn)
	// Shell exited — output stream EOF. Close conn to unblock stdin copy.
	conn.Close()
}

// Terminal raw mode via tcgetattr/tcsetattr.
type termios syscall.Termios

func makeRaw(fd uintptr) (*termios, error) {
	var old termios
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd,
		uintptr(getTermiosReq()), uintptr(unsafe.Pointer(&old)),
		0, 0, 0); errno != 0 {
		return nil, errno
	}
	raw := old
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK |
		syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON |
		syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd,
		uintptr(setTermiosReq()), uintptr(unsafe.Pointer(&raw)),
		0, 0, 0); errno != 0 {
		return nil, errno
	}
	return &old, nil
}

func restoreTerminal(fd uintptr, state *termios) {
	syscall.Syscall6(syscall.SYS_IOCTL, fd,
		uintptr(setTermiosReq()), uintptr(unsafe.Pointer(state)),
		0, 0, 0)
}

// Platform-specific ioctl numbers for termios.
// CLI runs on macOS (host).
func getTermiosReq() uint64 {
	return syscall.TIOCGETA // macOS: 0x40487413
}

func setTermiosReq() uint64 {
	return syscall.TIOCSETA // macOS: 0x80487414
}

// bridgePTYStreams connects agent PTY vsock streams to CLI PTY daemon streams.
// When agent dials port 1025, it sends the container name as a header line.
// The daemon holds the connection. When CLI connects to ptyDaemonSock with
// the same container name, the daemon bridges the two streams.
func bridgePTYStreams(agentPtyListener, cliPtyListener net.Listener) {
	pendingAgentConns := make(map[string]net.Conn)
	var mu sync.Mutex

	// Accept agent PTY connections.
	go func() {
		for {
			conn, err := agentPtyListener.Accept()
			if err != nil {
				return
			}
			// Read container name header (first line, newline-terminated).
			reader := bufio.NewReader(conn)
			line, err := reader.ReadString('\n')
			if err != nil {
				conn.Close()
				continue
			}
			name := strings.TrimSpace(line)
			mu.Lock()
			pendingAgentConns[name] = conn
			mu.Unlock()
			fmt.Printf("[pty] agent stream ready for %s\n", name)
		}
	}()

	// Accept CLI PTY connections and bridge.
	for {
		cliConn, err := cliPtyListener.Accept()
		if err != nil {
			return
		}
		// Read container name from CLI (newline-terminated).
		cliReader := bufio.NewReader(cliConn)
		line, err := cliReader.ReadString('\n')
		if err != nil {
			cliConn.Close()
			continue
		}
		name := strings.TrimSpace(line)

		mu.Lock()
		agentConn, ok := pendingAgentConns[name]
		if ok {
			delete(pendingAgentConns, name)
		}
		mu.Unlock()

		if !ok {
			fmt.Fprintf(os.Stderr, "[pty] no agent stream for %s\n", name)
			cliConn.Close()
			continue
		}

		// Bridge: CLI ↔ agent (raw bytes).
		// When either side closes (shell exit or CLI disconnect),
		// close BOTH connections to unblock the other io.Copy.
		go func() {
			ac, cc := agentConn, cliConn
			done := make(chan struct{}, 2)
			go func() { io.Copy(ac, cc); done <- struct{}{} }()
			go func() { io.Copy(cc, ac); done <- struct{}{} }()
			<-done        // first direction finished (shell exited or CLI disconnected)
			ac.Close()    // close both to unblock the other goroutine
			cc.Close()
			<-done        // wait for second goroutine
		}()
	}
}

func cmdContainerExec(name, command string) {
	resp, err := sendToDaemon(Request{
		Method: "container.exec",
		Params: map[string]interface{}{"name": name, "cmd": command},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
	var result ExecResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}
	if result.Stdout != "" {
		fmt.Print(result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}
	os.Exit(result.ExitCode)
}

func cmdContainerList() {
	resp, err := sendToDaemon(Request{Method: "container.list"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}

	var containers []map[string]interface{}
	if err := json.Unmarshal(resp.Result, &containers); err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}

	if len(containers) == 0 {
		fmt.Println("No containers.")
		return
	}
	fmt.Printf("%-16s %-10s %-8s %-8s %-16s %s\n", "NAME", "STATUS", "PID", "NETWORK", "IP", "IMAGE")
	for _, c := range containers {
		pid := ""
		if p, ok := c["pid"].(float64); ok && p > 0 {
			pid = fmt.Sprintf("%.0f", p)
		}
		ip, _ := c["ip"].(string)
		net, _ := c["network"].(string)
		fmt.Printf("%-16s %-10s %-8s %-8s %-16s %s\n",
			c["name"], c["status"], pid, net, ip, c["image"])
	}
}

func cmdImagePull(name string) {
	fmt.Printf("Pulling %s...\n", name)
	resp, err := sendToDaemon(Request{
		Method: "image.pull",
		Params: map[string]string{"name": name},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Printf("Image %s ready.\n", name)
}

func cmdImageList() {
	resp, err := sendToDaemon(Request{Method: "image.list"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
	var result map[string]interface{}
	json.Unmarshal(resp.Result, &result)
	fmt.Println("Available images:")
	if avail, ok := result["available"].([]interface{}); ok {
		for _, img := range avail {
			if m, ok := img.(map[string]interface{}); ok {
				fmt.Printf("  %-12s %s %s\n", m["name"], m["version"], m["arch"])
			}
		}
	}
}


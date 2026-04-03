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
	vsockDir       = "/tmp/aetheria-vsock-3"
	agentSock      = vsockDir + "/port-1024"
	ptySock        = vsockDir + "/port-1025" // PTY byte stream
	portForwardSock = vsockDir + "/port-1026" // port forward data channel

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
			fmt.Fprintln(os.Stderr, "usage: aetheria create <distro> [name] [--net=host|bridge|none] [-p host:container] [--memory=512m] [--cpus=1.0] [--pids=1024] [--restart=always]")
			os.Exit(1)
		}
		image := os.Args[2]
		// Derive container name from image: "nginx:1.25" → "nginx", "ghcr.io/owner/repo" → "repo"
		name := image
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if i := strings.Index(name, ":"); i >= 0 {
			name = name[:i]
		}
		network := "bridge" // default
		restart := "no"
		var ports []portMapping
		var envVars []string
		var volumes []map[string]interface{}
		var memoryMax int64
		var cpuMax float64
		var pidsMax int64
		for i := 3; i < len(os.Args); i++ {
			switch {
			case strings.HasPrefix(os.Args[i], "--net="):
				network = strings.TrimPrefix(os.Args[i], "--net=")
			case strings.HasPrefix(os.Args[i], "-p"):
				var portStr string
				if os.Args[i] == "-p" && i+1 < len(os.Args) {
					i++
					portStr = os.Args[i]
				} else {
					portStr = strings.TrimPrefix(os.Args[i], "-p")
				}
				pm, err := parsePortMapping(portStr)
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid port mapping %q: %v\n", portStr, err)
					os.Exit(1)
				}
				ports = append(ports, pm)
			case strings.HasPrefix(os.Args[i], "--memory="):
				v := strings.TrimPrefix(os.Args[i], "--memory=")
				memoryMax = parseMemorySize(v)
			case strings.HasPrefix(os.Args[i], "--cpus="):
				v := strings.TrimPrefix(os.Args[i], "--cpus=")
				fmt.Sscanf(v, "%f", &cpuMax)
			case strings.HasPrefix(os.Args[i], "--pids="):
				v := strings.TrimPrefix(os.Args[i], "--pids=")
				fmt.Sscanf(v, "%d", &pidsMax)
			case strings.HasPrefix(os.Args[i], "--restart="):
				restart = strings.TrimPrefix(os.Args[i], "--restart=")
			case strings.HasPrefix(os.Args[i], "-e"):
				var envStr string
				if os.Args[i] == "-e" && i+1 < len(os.Args) {
					i++
					envStr = os.Args[i]
				} else {
					envStr = strings.TrimPrefix(os.Args[i], "-e")
				}
				envVars = append(envVars, envStr)
			case strings.HasPrefix(os.Args[i], "-v"):
				var volStr string
				if os.Args[i] == "-v" && i+1 < len(os.Args) {
					i++
					volStr = os.Args[i]
				} else {
					volStr = strings.TrimPrefix(os.Args[i], "-v")
				}
				vm, err := parseVolumeMount(volStr)
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid volume %q: %v\n", volStr, err)
					os.Exit(1)
				}
				volumes = append(volumes, vm)
			default:
				if name == image {
					name = os.Args[i]
				}
			}
		}
		cmdContainerCreate(image, name, network, ports, memoryMax, cpuMax, pidsMax, restart, envVars, volumes)
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
	case "logs":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: aetheria logs <name> [-n lines]")
			os.Exit(1)
		}
		logName := os.Args[2]
		logLines := 100
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "-n" && i+1 < len(os.Args) {
				i++
				fmt.Sscanf(os.Args[i], "%d", &logLines)
			}
		}
		cmdContainerLogs(logName, logLines)
	case "rm":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: aetheria rm <name>")
			os.Exit(1)
		}
		cmdRemoveContainer(os.Args[2])
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
    [name]                       Container name (default: distro name)
    [--net=bridge|host|none]     Network mode (default: bridge)
    [-p host:container]          Port forwarding (repeatable)
    [--memory=512m]              Memory limit (e.g., 256m, 1g)
    [--cpus=1.0]                 CPU limit (e.g., 0.5, 2.0)
    [--pids=1024]                Max processes
    [--restart=always]           Auto-restart on VM boot
    [-e KEY=VALUE]               Environment variable (repeatable)
    [-v host:container[:ro]]     Volume mount (repeatable)
  aetheria start <name>        Start a container
  aetheria shell <name>        Open a shell in a running container
  aetheria exec <command>      Execute a command in the VM
  aetheria ls                  List containers
  aetheria logs <name> [-n N]  Show container logs (default: last 100 lines)
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

	// Port forward state.
	pfListeners    map[uint16]net.Listener // host port → TCP listener
	pendingPFConns map[string]net.Conn     // host_port string → pending agent vsock conn
	pfMu           sync.Mutex
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

	// Read response. Use long timeout for operations that may download
	// images (image.pull can take minutes on slow connections).
	d.agentConn.SetReadDeadline(time.Now().Add(10 * time.Minute))
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
	// Long timeout: image pull + extract can take minutes.
	conn.SetDeadline(time.Now().Add(10 * time.Minute))

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

	// Handle daemon-local commands.
	if req.Method == "_daemon.portforward.start" {
		d.handleDaemonPortForwardStart(conn, req)
		return
	}
	if req.Method == "_daemon.portforward.stop" {
		d.handleDaemonPortForwardStop(conn, req)
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

// handleDaemonPortForwardStart starts TCP listeners on the host for port forwards.
func (d *daemon) handleDaemonPortForwardStart(conn net.Conn, req Request) {
	var params struct {
		Ports []portMapping `json:"ports"`
	}
	raw, _ := json.Marshal(req.Params)
	if err := json.Unmarshal(raw, &params); err != nil {
		writeJSONResponse(conn, Response{Error: "invalid params: " + err.Error(), ID: req.ID})
		return
	}
	d.startPortForwards(params.Ports)
	writeJSONResponse(conn, Response{Result: json.RawMessage(`"ok"`), ID: req.ID})
}

// handleDaemonPortForwardStop closes TCP listeners for port forwards.
func (d *daemon) handleDaemonPortForwardStop(conn net.Conn, req Request) {
	var params struct {
		Ports []portMapping `json:"ports"`
	}
	raw, _ := json.Marshal(req.Params)
	json.Unmarshal(raw, &params)
	d.stopPortForwards(params.Ports)
	writeJSONResponse(conn, Response{Result: json.RawMessage(`"ok"`), ID: req.ID})
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
	os.Remove(portForwardSock)
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

	// 1d. Listen for agent port forward data connections (vsock port 1026)
	pfListener, err := net.Listen("unix", portForwardSock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "port forward listen: %v\n", err)
		os.Exit(1)
	}
	defer pfListener.Close()
	defer os.Remove(portForwardSock)

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
	dataDisk := os.Getenv("AETHERIA_DATA_DISK")
	args := []string{
		"run",
		"--mem", "512",
		"--cpus", "2",
		"--block", rootfs,
	}
	// Data disk: dedicated sparse ext4 for containers (appears as /dev/vdb).
	if dataDisk != "" {
		args = append(args, "--block", dataDisk)
	}
	args = append(args,
		"--serial", "type=stdout,hardware=serial,num=1",
		"-p", "root=/dev/vda rw console=ttyS0 earlycon=uart8250,mmio,0x3f8 loglevel=4",
	)
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

	// Start port forward bridge (accepts agent data connections on vsock port 1026).
	go d.bridgePortForwards(pfListener)

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

			// Restore port forwards for running containers (after VM restart).
			go d.restorePortForwards()
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

// portMapping mirrors the agent-side PortMapping for CLI use.
type portMapping struct {
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	Protocol      string `json:"protocol,omitempty"`
}

func parsePortMapping(s string) (portMapping, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return portMapping{}, fmt.Errorf("expected host:container format")
	}
	var hp, cp uint16
	if _, err := fmt.Sscanf(parts[0], "%d", &hp); err != nil {
		return portMapping{}, fmt.Errorf("invalid host port: %s", parts[0])
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &cp); err != nil {
		return portMapping{}, fmt.Errorf("invalid container port: %s", parts[1])
	}
	if hp == 0 || cp == 0 {
		return portMapping{}, fmt.Errorf("port numbers must be > 0")
	}
	return portMapping{HostPort: hp, ContainerPort: cp, Protocol: "tcp"}, nil
}

func parseMemorySize(s string) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	multiplier := int64(1)
	if strings.HasSuffix(s, "g") {
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	} else if strings.HasSuffix(s, "m") {
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	} else if strings.HasSuffix(s, "k") {
		multiplier = 1024
		s = s[:len(s)-1]
	}
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n * multiplier
}

func parseVolumeMount(s string) (map[string]interface{}, error) {
	// Format: host:container[:ro]
	parts := strings.SplitN(s, ":", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("expected host:container[:ro] format")
	}
	vm := map[string]interface{}{
		"host_path":      parts[0],
		"container_path": parts[1],
	}
	if len(parts) == 3 && parts[2] == "ro" {
		vm["read_only"] = true
	}
	return vm, nil
}

func cmdContainerCreate(image, name, network string, ports []portMapping, memoryMax int64, cpuMax float64, pidsMax int64, restart string, envVars []string, volumes []map[string]interface{}) {
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

	createParams := map[string]interface{}{
		"name":    name,
		"image":   image,
		"network": network,
		"restart": restart,
	}
	if len(ports) > 0 {
		createParams["ports"] = ports
	}
	if len(envVars) > 0 {
		createParams["env"] = envVars
	}
	if len(volumes) > 0 {
		createParams["volumes"] = volumes
	}
	if memoryMax > 0 || cpuMax > 0 || pidsMax > 0 {
		createParams["resources"] = map[string]interface{}{
			"memory_max": memoryMax,
			"cpu_max":    cpuMax,
			"pids_max":   pidsMax,
		}
	}

	fmt.Printf("Creating container %s (network=%s)...\n", name, network)
	resp, err = sendToDaemon(Request{
		Method: "container.create",
		Params: createParams,
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
	// Start port forwarding on the daemon if ports were specified.
	if len(ports) > 0 {
		resp, err = sendToDaemon(Request{
			Method: "_daemon.portforward.start",
			Params: map[string]interface{}{"ports": ports},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "port forward: %v\n", err)
		} else if resp.Error != "" {
			fmt.Fprintf(os.Stderr, "port forward: %s\n", resp.Error)
		} else {
			for _, p := range ports {
				fmt.Printf("Port forward: 0.0.0.0:%d → container:%d\n", p.HostPort, p.ContainerPort)
			}
		}
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
			// Read container name header byte-by-byte (same fix as port forward).
			// bufio.NewReader could buffer past the newline, losing data.
			name, err := readLineRaw(conn)
			if err != nil {
				conn.Close()
				continue
			}
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
		// Read container name from CLI (byte-by-byte, same reason as agent side).
		name, err := readLineRaw(cliConn)
		if err != nil {
			cliConn.Close()
			continue
		}

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

// ============================================================================
// Port forwarding (daemon side)
// ============================================================================

// startPortForwards sets up TCP listeners on the host for each port mapping.
// Called after a container is created with -p flags.
func (d *daemon) startPortForwards(ports []portMapping) {
	d.pfMu.Lock()
	if d.pfListeners == nil {
		d.pfListeners = make(map[uint16]net.Listener)
	}
	d.pfMu.Unlock()

	for _, pm := range ports {
		go d.listenPortForward(pm.HostPort)
	}
}

// listenPortForward listens on a host TCP port and tunnels connections to the agent.
func (d *daemon) listenPortForward(hostPort uint16) {
	addr := fmt.Sprintf("0.0.0.0:%d", hostPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[portforward] listen %s: %v\n", addr, err)
		return
	}

	d.pfMu.Lock()
	d.pfListeners[hostPort] = ln
	d.pfMu.Unlock()

	fmt.Printf("[portforward] listening on %s\n", addr)

	for {
		tcpConn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go d.handlePortForwardConn(tcpConn, hostPort)
	}
}

// handlePortForwardConn handles a single incoming TCP connection on a forwarded port.
// Sends RPC to agent, waits for agent to dial vsock data channel, bridges.
func (d *daemon) handlePortForwardConn(tcpConn net.Conn, hostPort uint16) {
	// 1. Tell agent to establish tunnel.
	resp, err := d.sendToAgent(Request{
		Method: "portforward.connect",
		Params: map[string]interface{}{"host_port": hostPort},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[portforward] RPC error for port %d: %v\n", hostPort, err)
		tcpConn.Close()
		return
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "[portforward] agent error for port %d: %s\n", hostPort, resp.Error)
		tcpConn.Close()
		return
	}

	// 2. Wait for agent to dial vsock port 1026 with matching header.
	// bridgePortForwards() accepts the vsock connection and stores it
	// in pendingPFConns keyed by host_port. Poll until available.
	key := fmt.Sprintf("%d", hostPort)
	var agentConn net.Conn
	for i := 0; i < 50; i++ {
		d.pfMu.Lock()
		if conn, ok := d.pendingPFConns[key]; ok {
			delete(d.pendingPFConns, key)
			agentConn = conn
		}
		d.pfMu.Unlock()
		if agentConn != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if agentConn == nil {
		fmt.Fprintf(os.Stderr, "[portforward] timeout waiting for agent data channel on port %d\n", hostPort)
		tcpConn.Close()
		return
	}

	// 3. Bridge: TCP client ↔ agent vsock data channel.
	done := make(chan struct{}, 2)
	go func() { io.Copy(agentConn, tcpConn); done <- struct{}{} }()
	go func() { io.Copy(tcpConn, agentConn); done <- struct{}{} }()
	<-done
	agentConn.Close()
	tcpConn.Close()
	<-done
}

// stopPortForwards closes all TCP listeners for the given ports.
func (d *daemon) stopPortForwards(ports []portMapping) {
	d.pfMu.Lock()
	defer d.pfMu.Unlock()
	for _, pm := range ports {
		if ln, ok := d.pfListeners[pm.HostPort]; ok {
			ln.Close()
			delete(d.pfListeners, pm.HostPort)
			fmt.Printf("[portforward] stopped listening on port %d\n", pm.HostPort)
		}
	}
}

// bridgePortForwards accepts agent data connections on the port-1026 vsock socket
// and stores them for matching with incoming TCP clients.
func (d *daemon) bridgePortForwards(pfListener net.Listener) {
	d.pfMu.Lock()
	d.pendingPFConns = make(map[string]net.Conn)
	d.pfMu.Unlock()

	for {
		conn, err := pfListener.Accept()
		if err != nil {
			return // listener closed
		}
		// Read header byte-by-byte to avoid buffering past the newline.
		// bufio.NewReader could read ahead, causing data loss when we
		// later bridge the raw conn.
		key, err := readLineRaw(conn)
		if err != nil {
			conn.Close()
			continue
		}
		d.pfMu.Lock()
		d.pendingPFConns[key] = conn
		d.pfMu.Unlock()
	}
}

// readLineRaw reads a newline-terminated line from a connection byte by byte.
// Does not buffer past the newline, so the connection can be safely bridged after.
func readLineRaw(conn net.Conn) (string, error) {
	var buf []byte
	b := make([]byte, 1)
	for {
		n, err := conn.Read(b)
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}
		if b[0] == '\n' {
			return strings.TrimSpace(string(buf)), nil
		}
		buf = append(buf, b[0])
		if len(buf) > 256 {
			return "", fmt.Errorf("header too long")
		}
	}
}

// restorePortForwards queries running containers and restores port forward
// TCP listeners for any containers that have port mappings configured.
func (d *daemon) restorePortForwards() {
	resp, err := d.sendToAgent(Request{Method: "container.list"})
	if err != nil {
		return
	}

	var containers []map[string]interface{}
	if json.Unmarshal(resp.Result, &containers) != nil {
		return
	}

	for _, c := range containers {
		status, _ := c["status"].(string)
		if status != "running" {
			continue
		}
		ps, ok := c["ports"].([]interface{})
		if !ok || len(ps) == 0 {
			continue
		}
		var ports []portMapping
		for _, p := range ps {
			if pm, ok := p.(map[string]interface{}); ok {
				hp, _ := pm["host_port"].(float64)
				cp, _ := pm["container_port"].(float64)
				if hp > 0 && cp > 0 {
					ports = append(ports, portMapping{HostPort: uint16(hp), ContainerPort: uint16(cp)})
				}
			}
		}
		if len(ports) > 0 {
			cname, _ := c["name"].(string)
			fmt.Printf("[portforward] restoring port forwards for %s\n", cname)
			d.startPortForwards(ports)
		}
	}
}

func cmdContainerLogs(name string, lines int) {
	resp, err := sendToDaemon(Request{
		Method: "container.logs",
		Params: map[string]interface{}{"name": name, "lines": lines},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		os.Exit(1)
	}
	var logs string
	json.Unmarshal(resp.Result, &logs)
	fmt.Print(logs)
}

func cmdRemoveContainer(name string) {
	// Query container list to find port forwards before stopping.
	resp, err := sendToDaemon(Request{Method: "container.list"})
	if err == nil && resp.Error == "" {
		var containers []map[string]interface{}
		if json.Unmarshal(resp.Result, &containers) == nil {
			for _, c := range containers {
				cname, _ := c["name"].(string)
				if cname != name {
					continue
				}
				if ps, ok := c["ports"].([]interface{}); ok && len(ps) > 0 {
					var ports []portMapping
					for _, p := range ps {
						if pm, ok := p.(map[string]interface{}); ok {
							hp, _ := pm["host_port"].(float64)
							cp, _ := pm["container_port"].(float64)
							ports = append(ports, portMapping{HostPort: uint16(hp), ContainerPort: uint16(cp)})
						}
					}
					if len(ports) > 0 {
						sendToDaemon(Request{
							Method: "_daemon.portforward.stop",
							Params: map[string]interface{}{"ports": ports},
						})
					}
				}
			}
		}
	}
	cmdContainerAction("container.stop", name)
	cmdContainerAction("container.remove", name)
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
	fmt.Printf("%-16s %-10s %-8s %-8s %-16s %-20s %s\n", "NAME", "STATUS", "PID", "NETWORK", "IP", "PORTS", "IMAGE")
	for _, c := range containers {
		pid := ""
		if p, ok := c["pid"].(float64); ok && p > 0 {
			pid = fmt.Sprintf("%.0f", p)
		}
		ip, _ := c["ip"].(string)
		netMode, _ := c["network"].(string)
		portsStr := ""
		if ps, ok := c["ports"].([]interface{}); ok && len(ps) > 0 {
			var parts []string
			for _, p := range ps {
				if pm, ok := p.(map[string]interface{}); ok {
					hp, _ := pm["host_port"].(float64)
					cp, _ := pm["container_port"].(float64)
					parts = append(parts, fmt.Sprintf("%.0f:%.0f", hp, cp))
				}
			}
			portsStr = strings.Join(parts, ",")
		}
		fmt.Printf("%-16s %-10s %-8s %-8s %-16s %-20s %s\n",
			c["name"], c["status"], pid, netMode, ip, portsStr, c["image"])
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


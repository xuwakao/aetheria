//go:build linux

// aetheria-agent — runs inside the guest VM.
//
// Connects to the host via vsock (CID=2, port=1024) and processes commands
// sent as JSON-RPC over the connection. Designed to be started as a service
// or from /etc/local.d/ on Alpine Linux.
//
// Communication flow:
//   agent (guest) → vsock CID=2:1024 → crosvm → Unix socket → host CLI/daemon

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	hostCID    = 2    // vsock host CID (always 2 per spec)
	agentPort  = 1024 // vsock port for agent communication
	retryDelay = 2 * time.Second
	maxRetries = 30 // 60 seconds total retry window
)

// Request is a JSON-RPC request from the host.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	ID     int             `json:"id"`
}

// Response is a JSON-RPC response to the host.
type Response struct {
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
	ID     int         `json:"id"`
}

// ExecParams for the "exec" method.
type ExecParams struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args,omitempty"`
}

// ExecResult returned by the "exec" method.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// InfoResult returned by the "info" method.
type InfoResult struct {
	Hostname string `json:"hostname"`
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`
	Uptime   string `json:"uptime"`
	GoVer    string `json:"go_version"`
}

// Global container manager, initialized in main().
var containers *ContainerManager

func main() {
	// If re-exec'd as container init, run container setup and never return.
	if isContainerInit() {
		containerInit()
		os.Exit(1) // unreachable
	}

	log.SetPrefix("[agent] ")
	log.SetFlags(log.Ltime)

	log.Println("aetheria-agent starting")

	containers = NewContainerManager()

	// Reconnection loop: if host disconnects (e.g., daemon restart),
	// agent reconnects automatically.
	for {
		conn := connectToHost()
		log.Println("connected to host via vsock")
		handleConnection(conn)
		conn.Close()
		log.Println("host disconnected, reconnecting in 2s...")
		time.Sleep(2 * time.Second)
	}
}

// connectToHost dials the host via AF_VSOCK with retries.
func connectToHost() *vsockConn {
	for i := 0; i < maxRetries; i++ {
		conn, err := dialVsock(hostCID, agentPort)
		if err == nil {
			return conn
		}
		if i%5 == 0 {
			log.Printf("waiting for host (attempt %d/%d): %v", i+1, maxRetries, err)
		}
		time.Sleep(retryDelay)
	}
	log.Fatal("failed to connect to host after retries")
	return nil
}

// vsockConn wraps a raw vsock fd as a net.Conn-like reader/writer.
// Go's net.FileConn doesn't support AF_VSOCK, so we use the raw fd directly.
type vsockConn struct {
	fd int
}

func (c *vsockConn) Read(b []byte) (int, error) {
	n, err := syscall.Read(c.fd, b)
	if n == 0 && err == nil {
		return 0, fmt.Errorf("connection closed")
	}
	return n, err
}

func (c *vsockConn) Write(b []byte) (int, error) {
	return syscall.Write(c.fd, b)
}

func (c *vsockConn) Close() error {
	// Shutdown first: triggers kernel vsock to send VIRTIO_VSOCK_OP_SHUTDOWN
	// through the virtio TX queue. Without this, close() alone doesn't
	// notify the host, and the host-side Unix socket never gets EOF.
	syscall.Shutdown(c.fd, syscall.SHUT_RDWR)
	return syscall.Close(c.fd)
}

// dialVsock creates an AF_VSOCK connection to the given CID and port.
func dialVsock(cid, port uint32) (*vsockConn, error) {
	// AF_VSOCK = 40 on Linux (defined in linux/socket.h).
	// Go's syscall package doesn't export this constant.
	const afVsock = 40

	fd, err := syscall.Socket(afVsock, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}

	// sockaddr_vm layout on Linux: family(2) + reserved(2) + port(4) + cid(4) + zero(4) = 16 bytes
	sa := [16]byte{}
	sa[0] = afVsock
	sa[1] = 0
	// port (little-endian u32 at offset 4)
	sa[4] = byte(port)
	sa[5] = byte(port >> 8)
	sa[6] = byte(port >> 16)
	sa[7] = byte(port >> 24)
	// cid (little-endian u32 at offset 8)
	sa[8] = byte(cid)
	sa[9] = byte(cid >> 8)
	sa[10] = byte(cid >> 16)
	sa[11] = byte(cid >> 24)

	_, _, errno := syscall.RawSyscall(
		syscall.SYS_CONNECT,
		uintptr(fd),
		uintptr(unsafe.Pointer(&sa[0])),
		16,
	)
	if errno != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("connect: %v", errno)
	}

	return &vsockConn{fd: fd}, nil
}

// handleConnection processes JSON-RPC requests from the host.
func handleConnection(conn *vsockConn) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max message

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			sendResponse(conn, Response{Error: "invalid JSON: " + err.Error(), ID: 0})
			continue
		}

		resp := handleRequest(req)
		sendResponse(conn, resp)
	}

	if err := scanner.Err(); err != nil {
		log.Printf("connection error: %v", err)
	}
	log.Println("host disconnected")
}

func handleRequest(req Request) Response {
	// Route container.*, image.*, and shell RPCs.
	if req.Method == "container.shell" {
		return handleShellRPC(req)
	}
	if strings.HasPrefix(req.Method, "container.") {
		return containers.HandleRPC(req)
	}
	if strings.HasPrefix(req.Method, "image.") {
		return handleImageRPC(req)
	}
	if strings.HasPrefix(req.Method, "portforward.") {
		return handlePortForwardRPC(req)
	}

	switch req.Method {
	case "ping":
		return Response{Result: "pong", ID: req.ID}

	case "info":
		return handleInfo(req.ID)

	case "exec":
		return handleExec(req)

	default:
		return Response{Error: fmt.Sprintf("unknown method: %s", req.Method), ID: req.ID}
	}
}

func handleInfo(id int) Response {
	hostname, _ := os.Hostname()
	var uname syscall.Utsname
	syscall.Uname(&uname)

	kernel := ""
	for _, b := range uname.Release {
		if b == 0 {
			break
		}
		kernel += string(rune(b))
	}

	uptime := ""
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) > 0 {
			uptime = parts[0] + "s"
		}
	}

	return Response{
		Result: InfoResult{
			Hostname: hostname,
			Kernel:   kernel,
			Arch:     runtime.GOARCH,
			Uptime:   uptime,
			GoVer:    runtime.Version(),
		},
		ID: id,
	}
}

func handleExec(req Request) Response {
	var params ExecParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return Response{Error: "invalid exec params: " + err.Error(), ID: req.ID}
	}

	if params.Cmd == "" {
		return Response{Error: "cmd is required", ID: req.ID}
	}

	// Use sh -c for shell interpretation if no explicit args
	var cmd *exec.Cmd
	if len(params.Args) == 0 {
		cmd = exec.Command("/bin/sh", "-c", params.Cmd)
	} else {
		cmd = exec.Command(params.Cmd, params.Args...)
	}

	// Cap output to prevent OOM on large command output.
	const maxOutput = 10 * 1024 * 1024 // 10MB
	var stdout, stderr limitedWriter
	stdout.limit = maxOutput
	stderr.limit = maxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return Response{Error: fmt.Sprintf("exec failed: %v", err), ID: req.ID}
		}
	}

	return Response{
		Result: ExecResult{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: exitCode,
		},
		ID: req.ID,
	}
}

// limitedWriter caps the amount of data that can be written.
// Prevents OOM from commands with excessive output.
type limitedWriter struct {
	buf       []byte
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - len(w.buf)
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil // accept but discard
	}
	if len(p) > remaining {
		w.buf = append(w.buf, p[:remaining]...)
		w.truncated = true
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *limitedWriter) String() string {
	s := string(w.buf)
	if w.truncated {
		s += "\n... [output truncated at 10MB]"
	}
	return s
}

func sendResponse(conn *vsockConn, resp Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("marshal error: %v", err)
		return
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		log.Printf("write error: %v", err)
	}
}

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
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// vsock agent connection: crosvm maps vsock CID=2 port=1024 to this Unix socket.
	vsockDir  = "/tmp/aetheria-vsock-3"
	agentSock = vsockDir + "/port-1024"

	// Daemon control socket: CLI commands connect here.
	daemonSock = "/tmp/aetheria.sock"
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
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `aetheria — lightweight Linux VM runtime

Usage:
  aetheria run              Start the VM (daemon mode)
  aetheria exec <command>   Execute a command in the VM
  aetheria ping             Check if VM agent is running
  aetheria info             Show VM information
  aetheria stop             Shutdown the VM`)
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

	d.idCounter++
	req.ID = d.idCounter

	data, _ := json.Marshal(req)
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
		resp, _ := json.Marshal(Response{Error: "invalid request"})
		conn.Write(append(resp, '\n'))
		return
	}

	// Forward to agent
	resp, err := d.sendToAgent(req)
	if err != nil {
		errResp, _ := json.Marshal(Response{Error: err.Error(), ID: req.ID})
		conn.Write(append(errResp, '\n'))
		return
	}

	data, _ := json.Marshal(resp)
	conn.Write(append(data, '\n'))
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
	os.Remove(daemonSock)

	// 1. Listen for agent vsock connection
	agentListener, err := net.Listen("unix", agentSock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent listen: %v\n", err)
		os.Exit(1)
	}
	defer agentListener.Close()

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
		"run", "--no-usb",
		"--serial", "type=stdout,hardware=serial,num=1",
		"--rwdisk", rootfs,
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
	fmt.Print("Waiting for agent...")
	agentCh := make(chan net.Conn, 1)
	go func() {
		conn, err := agentListener.Accept()
		if err == nil {
			agentCh <- conn
		}
	}()

	var agentConn net.Conn
	select {
	case agentConn = <-agentCh:
		fmt.Println(" connected!")
	case <-time.After(60 * time.Second):
		fmt.Println(" timeout!")
		cmd.Process.Kill()
		os.Exit(1)
	}
	defer agentConn.Close()

	d := &daemon{
		agentConn:   agentConn,
		agentReader: bufio.NewReaderSize(agentConn, 1024*1024),
	}

	// Verify with ping
	resp, err := d.sendToAgent(Request{Method: "ping"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent ping failed: %v\n", err)
		cmd.Process.Kill()
		os.Exit(1)
	}
	fmt.Printf("Agent ready: %s\n", string(resp.Result))
	fmt.Println("VM running. Use 'aetheria exec <cmd>' in another terminal.")

	// 5. Accept CLI connections and relay to agent
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

	data, _ := json.Marshal(req)
	conn.Write(append(data, '\n'))

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no response from daemon")
	}

	var resp Response
	json.Unmarshal(scanner.Bytes(), &resp)
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
	json.Unmarshal(resp.Result, &info)
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
	json.Unmarshal(resp.Result, &result)

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

	data, _ := json.Marshal(Request{Method: "exec", Params: map[string]string{"cmd": "poweroff"}})
	conn.Write(append(data, '\n'))

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


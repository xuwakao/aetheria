// aetheria — macOS host CLI for managing the Aetheria VM.
//
// Commands:
//   aetheria run     — start VM, wait for agent connection
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
	"syscall"
	"time"
)

const (
	vsockDir  = "/tmp/aetheria-vsock-3"
	agentSock = vsockDir + "/port-1024"
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
  aetheria run              Start the VM and wait for agent
  aetheria exec <command>   Execute a command in the VM
  aetheria ping             Check if VM agent is running
  aetheria info             Show VM information
  aetheria stop             Shutdown the VM`)
}

// cmdRun starts crosvm and waits for the agent to connect.
func cmdRun() {
	// Paths (configurable later via flags)
	crosvmBin := os.Getenv("AETHERIA_CROSVM")
	if crosvmBin == "" {
		// Default: look relative to this binary or common paths
		candidates := []string{
			"aetheria-crosvm/target/release/crosvm",
			"/usr/local/bin/crosvm",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				crosvmBin = c
				break
			}
		}
	}
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

	// Prepare vsock socket directory
	os.MkdirAll(vsockDir, 0755)
	os.Remove(agentSock)

	// Start listening BEFORE crosvm (agent connects after boot)
	listener, err := net.Listen("unix", agentSock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen error: %v\n", err)
		os.Exit(1)
	}
	defer listener.Close()

	// Build crosvm command
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
		fmt.Fprintf(os.Stderr, "failed to start crosvm: %v\n", err)
		os.Exit(1)
	}

	// Handle Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutting down VM...")
		cmd.Process.Signal(syscall.SIGTERM)
	}()

	// Wait for agent connection (with timeout)
	fmt.Print("Waiting for agent...")
	connCh := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			connCh <- conn
		}
	}()

	select {
	case conn := <-connCh:
		fmt.Println(" connected!")
		// Send ping to verify
		conn.Write([]byte(`{"method":"ping","id":0}` + "\n"))
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		fmt.Printf("Agent ready: %s\n", strings.TrimSpace(string(buf[:n])))
		conn.Close()
		fmt.Println("VM is running. Use 'aetheria exec <cmd>' in another terminal.")
	case <-time.After(60 * time.Second):
		fmt.Println(" timeout!")
		fmt.Fprintln(os.Stderr, "Agent did not connect within 60 seconds")
		cmd.Process.Kill()
		os.Exit(1)
	}

	// Keep running until crosvm exits
	cmd.Wait()
}

// sendCommand connects to the agent socket, sends a request, and returns the response.
func sendCommand(req Request) (*Response, error) {
	conn, err := net.DialTimeout("unix", agentSock, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to agent (is VM running?): %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	data, _ := json.Marshal(req)
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("send: %v", err)
	}

	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return nil, fmt.Errorf("no response from agent")
	}

	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("invalid response: %v", err)
	}
	return &resp, nil
}

func cmdPing() {
	resp, err := sendCommand(Request{Method: "ping", ID: 1})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(resp.Result))
}

func cmdInfo() {
	resp, err := sendCommand(Request{Method: "info", ID: 1})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	// Pretty-print info
	var info map[string]interface{}
	json.Unmarshal(resp.Result, &info)
	for k, v := range info {
		fmt.Printf("%-12s %v\n", k+":", v)
	}
}

func cmdExec(command string) {
	resp, err := sendCommand(Request{
		Method: "exec",
		Params: map[string]string{"cmd": command},
		ID:     1,
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
	resp, err := sendCommand(Request{
		Method: "exec",
		Params: map[string]string{"cmd": "poweroff"},
		ID:     1,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
	} else {
		fmt.Println("Shutdown signal sent")
	}
}

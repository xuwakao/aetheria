//go:build linux

// shell.go — Interactive shell RPC handler and PTY vsock stream listener.
//
// Flow:
// 1. Host sends container.shell RPC via JSON-RPC (port 1024)
// 2. Agent starts PTY + nsenter shell, stores session
// 3. Host opens a second vsock connection to port 1025
// 4. Agent accepts the connection, links it to the PTY session
// 5. Raw bytes flow bidirectionally until shell exits

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
)

const (
	ptyPort = 1025 // vsock port for PTY byte streams
)

// shellSessions tracks active PTY sessions waiting for stream connections.
var (
	shellMu       sync.Mutex
	pendingShells = make(map[string]*ShellSession) // keyed by container name
)

// ContainerShellParams for container.shell RPC.
type ContainerShellParams struct {
	Name string `json:"name"`
	Rows uint16 `json:"rows,omitempty"` // initial terminal rows (default 24)
	Cols uint16 `json:"cols,omitempty"` // initial terminal cols (default 80)
}

// handleShellRPC creates a PTY session and waits for the stream connection.
func handleShellRPC(req Request) Response {
	var params ContainerShellParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return Response{Error: "invalid params: " + err.Error(), ID: req.ID}
	}

	// Look up running container.
	containers.mu.Lock()
	c, ok := containers.containers[params.Name]
	if !ok {
		containers.mu.Unlock()
		return Response{Error: fmt.Sprintf("container %q not found", params.Name), ID: req.ID}
	}
	if c.Status != "running" {
		containers.mu.Unlock()
		return Response{Error: fmt.Sprintf("container %q is not running", params.Name), ID: req.ID}
	}
	pid := c.Pid
	network := c.Network
	containers.mu.Unlock()

	// Start PTY + shell.
	session, err := StartShell(params.Name, pid, network)
	if err != nil {
		return Response{Error: fmt.Sprintf("start shell: %v", err), ID: req.ID}
	}

	// Set initial window size.
	if params.Rows > 0 && params.Cols > 0 {
		session.Resize(params.Rows, params.Cols)
	}

	// Store session for the PTY stream connection to pick up.
	shellMu.Lock()
	pendingShells[params.Name] = session
	shellMu.Unlock()

	log.Printf("[shell] PTY session created for %s, connecting stream on port %d", params.Name, ptyPort)

	// Connect the PTY stream in background (don't block the RPC response).
	go connectShellStream(session)

	return Response{Result: map[string]interface{}{
		"status": "ready",
		"port":   ptyPort,
	}, ID: req.ID}
}

// listenPTYStreams accepts vsock connections on the PTY port and links
// them to pending shell sessions. The first line received on the connection
// is the container name (to match the session).
func listenPTYStreams() {
	// Listen on vsock port 1025 for PTY stream connections.
	// Unlike the RPC port (which the agent dials to the host),
	// the PTY port is a listener — the host dials to the agent.
	//
	// Actually, our vsock implementation has the agent DIAL the host.
	// For the PTY stream, we also dial the host on port 1025.
	// The host daemon listens on that port (via crosvm vsock mapping).
	//
	// But this is asymmetric: the shell RPC tells the host "ready on port 1025",
	// then the HOST should connect to the AGENT's port 1025. In crosvm's
	// userspace vsock, the guest connects to host CID=2, and the host
	// gets a Unix socket. There's no way for the host to initiate connection
	// TO the guest.
	//
	// Solution: the agent dials host port 1025 after creating the session.
	// The host daemon accepts the connection on the vsock Unix socket.
	// This matches the existing dial pattern (agent→host).

	log.Printf("[shell] PTY stream handler ready (agent dials host on port %d)", ptyPort)
	// This goroutine doesn't actively listen — instead, after creating a
	// session, the agent dials host:1025 in handleShellStream().
}

// connectShellStream dials the host on the PTY port and streams PTY I/O.
// Called after a shell session is created.
func connectShellStream(session *ShellSession) {
	conn, err := dialVsock(hostCID, ptyPort)
	if err != nil {
		log.Printf("[shell] failed to connect PTY stream for %s: %v", session.containerName, err)
		return
	}

	// Send container name as the first line so the host can identify the session.
	header := session.containerName + "\n"
	conn.Write([]byte(header))

	log.Printf("[shell] PTY stream connected for %s", session.containerName)

	// Block: stream PTY ↔ vsock until shell exits.
	session.StreamTo(conn)
}

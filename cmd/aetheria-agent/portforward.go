//go:build linux

// portforward.go — Port forwarding via vsock tunnel.
//
// When the host daemon receives a TCP connection on a forwarded port, it sends
// a portforward.connect RPC to the agent. The agent looks up the mapping,
// dials the container's IP:port, and dials vsock CID=2:1026 to create a data
// channel. The host daemon bridges the TCP client to the vsock data channel.
//
// Protocol per forwarded connection:
//   1. Host → agent RPC: portforward.connect {host_port: N}
//   2. Agent dials container_ip:container_port (TCP)
//   3. Agent dials vsock CID=2:1026, sends header: "{host_port}\n"
//   4. Agent bridges vsock ↔ container TCP (bidirectional io.Copy)
//   5. Host bridges TCP client ↔ vsock data channel

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"
)

const portForwardVsockPort = 1026

// portForwardConnectParams is the RPC parameter for portforward.connect.
type portForwardConnectParams struct {
	HostPort uint16 `json:"host_port"`
}

// handlePortForwardRPC routes port forward RPC methods.
func handlePortForwardRPC(req Request) Response {
	switch req.Method {
	case "portforward.connect":
		return handlePortForwardConnect(req)
	case "portforward.list":
		return handlePortForwardList(req)
	default:
		return Response{Error: fmt.Sprintf("unknown portforward method: %s", req.Method), ID: req.ID}
	}
}

// handlePortForwardConnect establishes a tunnel for one TCP connection.
// Called when the host daemon receives a new TCP connection on a forwarded port.
func handlePortForwardConnect(req Request) Response {
	var params portForwardConnectParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return Response{Error: "invalid params: " + err.Error(), ID: req.ID}
	}

	// Find the container and port mapping.
	containerName, containerPort, containerIP, err := lookupPortForward(params.HostPort)
	if err != nil {
		return Response{Error: err.Error(), ID: req.ID}
	}

	// Dial the container's port with timeout to avoid blocking
	// the RPC handler (and all other RPCs) if the port isn't listening.
	target := fmt.Sprintf("%s:%d", containerIP, containerPort)
	containerConn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return Response{Error: fmt.Sprintf("connect to container %s at %s: %v", containerName, target, err), ID: req.ID}
	}

	// Dial vsock data channel to host.
	vsockConn, err := dialVsock(hostCID, portForwardVsockPort)
	if err != nil {
		containerConn.Close()
		return Response{Error: fmt.Sprintf("dial vsock data channel: %v", err), ID: req.ID}
	}

	// Send header: host_port so the daemon can match this to the pending TCP client.
	header := strconv.FormatUint(uint64(params.HostPort), 10) + "\n"
	if _, err := vsockConn.Write([]byte(header)); err != nil {
		containerConn.Close()
		vsockConn.Close()
		return Response{Error: fmt.Sprintf("write vsock header: %v", err), ID: req.ID}
	}

	// Bridge in background: vsock ↔ container TCP.
	go bridgeConnections(vsockConn, containerConn, containerName, params.HostPort)

	log.Printf("[portforward] tunnel established: host:%d → %s:%d (%s)", params.HostPort, containerIP, containerPort, containerName)
	return Response{Result: "connected", ID: req.ID}
}

// handlePortForwardList returns all active port forward mappings.
func handlePortForwardList(req Request) Response {
	containers.mu.Lock()
	defer containers.mu.Unlock()

	type pfInfo struct {
		Container     string `json:"container"`
		HostPort      uint16 `json:"host_port"`
		ContainerPort uint16 `json:"container_port"`
		ContainerIP   string `json:"container_ip"`
	}

	var forwards []pfInfo
	for _, c := range containers.containers {
		for _, p := range c.Ports {
			forwards = append(forwards, pfInfo{
				Container:     c.Name,
				HostPort:      p.HostPort,
				ContainerPort: p.ContainerPort,
				ContainerIP:   c.IP,
			})
		}
	}
	return Response{Result: forwards, ID: req.ID}
}

// lookupPortForward finds the container and target port for a given host port.
func lookupPortForward(hostPort uint16) (containerName string, containerPort uint16, containerIP string, err error) {
	containers.mu.Lock()
	defer containers.mu.Unlock()

	for _, c := range containers.containers {
		if c.Status != "running" {
			continue
		}
		for _, p := range c.Ports {
			if p.HostPort == hostPort {
				ip := c.IP
				if ip == "" {
					// Host mode: container shares VM's network namespace.
					// Services listen on localhost.
					if c.Network == NetHost {
						ip = "127.0.0.1"
					} else {
						return "", 0, "", fmt.Errorf("container %q has no IP (network mode: %s)", c.Name, c.Network)
					}
				}
				return c.Name, p.ContainerPort, ip, nil
			}
		}
	}
	return "", 0, "", fmt.Errorf("no port forward mapping for host port %d", hostPort)
}

// bridgeConnections copies data bidirectionally between two connections.
// Closes both connections when either direction finishes.
func bridgeConnections(vsock *vsockConn, container net.Conn, name string, hostPort uint16) {
	var once sync.Once
	closeAll := func() {
		vsock.Close()
		container.Close()
	}

	done := make(chan struct{}, 2)

	// vsock → container
	go func() {
		buf := make([]byte, 32*1024)
		io.CopyBuffer(container, vsock, buf)
		once.Do(closeAll)
		done <- struct{}{}
	}()

	// container → vsock
	go func() {
		buf := make([]byte, 32*1024)
		io.CopyBuffer(vsock, container, buf)
		once.Do(closeAll)
		done <- struct{}{}
	}()

	<-done
	<-done
	log.Printf("[portforward] tunnel closed: host:%d → %s", hostPort, name)
}

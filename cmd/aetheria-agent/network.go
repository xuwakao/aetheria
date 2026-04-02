//go:build linux

// network.go — Container bridge networking.
//
// Sets up per-container network isolation using veth pairs, a Linux bridge,
// and iptables NAT. Each container in bridge mode gets its own IP on the
// 10.42.0.0/24 subnet.
//
// Architecture:
//   VM eth0 (192.168.64.x) ← vmnet NAT → macOS → internet
//       ↕ iptables MASQUERADE
//   br-aetheria (10.42.0.1/24)
//       ↕ veth pair per container
//   container eth0 (10.42.0.N/24) → default via 10.42.0.1

package main

import (
	"fmt"
	"log"
	"os/exec"
	"sync"
)

const (
	bridgeName   = "br-aetheria"
	bridgeSubnet = "10.42.0"
	bridgeIP     = bridgeSubnet + ".1"
	bridgeCIDR   = bridgeIP + "/24"
)

var (
	bridgeOnce sync.Once
	nextIP     uint8 = 2 // start from 10.42.0.2
	ipMu       sync.Mutex
)

// allocateIP returns the next available container IP.
func allocateIP() string {
	ipMu.Lock()
	defer ipMu.Unlock()
	ip := fmt.Sprintf("%s.%d", bridgeSubnet, nextIP)
	nextIP++
	if nextIP > 254 {
		nextIP = 2 // wrap (simple; production would track freed IPs)
	}
	return ip
}

// ensureBridge creates the bridge interface and enables NAT (once).
func ensureBridge() error {
	var err error
	bridgeOnce.Do(func() {
		log.Printf("[network] creating bridge %s (%s)", bridgeName, bridgeCIDR)

		// Create bridge.
		if e := run("ip", "link", "add", bridgeName, "type", "bridge"); e != nil {
			// Bridge may already exist from a previous run.
			log.Printf("[network] bridge create: %v (may already exist)", e)
		}
		if e := run("ip", "addr", "add", bridgeCIDR, "dev", bridgeName); e != nil {
			log.Printf("[network] bridge addr: %v (may already exist)", e)
		}
		if e := run("ip", "link", "set", bridgeName, "up"); e != nil {
			err = fmt.Errorf("bridge up: %w", e)
			return
		}

		// Enable IP forwarding.
		if e := run("sh", "-c", "echo 1 > /proc/sys/net/ipv4/ip_forward"); e != nil {
			log.Printf("[network] ip_forward: %v", e)
		}

		// NAT: masquerade traffic from bridge subnet.
		// Use nft (nftables) with full path. Alpine's iptables uses nftables
		// backend which has compatibility issues with MASQUERADE.
		nft := "/usr/sbin/nft"
		if e := run(nft, "add", "table", "ip", "aetheria_nat"); e != nil {
			log.Printf("[network] nft table: %v", e)
		}
		run(nft, "add", "chain", "ip", "aetheria_nat", "postrouting",
			"{ type nat hook postrouting priority 100 ; }")
		if e := run(nft, "add", "rule", "ip", "aetheria_nat", "postrouting",
			"ip", "saddr", bridgeSubnet+".0/24", "masquerade"); e != nil {
			// Fallback: iptables-legacy (uses old kernel API, more reliable)
			log.Printf("[network] nft masquerade failed (%v), trying iptables-legacy", e)
			run("/usr/sbin/iptables-legacy", "-t", "nat", "-A", "POSTROUTING",
				"-s", bridgeSubnet+".0/24", "!", "-o", bridgeName,
				"-j", "MASQUERADE")
		}

		// Allow forwarding between bridge and external.
		run(nft, "add", "chain", "ip", "aetheria_nat", "forward",
			"{ type filter hook forward priority 0 ; }")
		run(nft, "add", "rule", "ip", "aetheria_nat", "forward",
			"iifname", bridgeName, "accept")
		run(nft, "add", "rule", "ip", "aetheria_nat", "forward",
			"oifname", bridgeName, "ct", "state", "established,related", "accept")

		log.Printf("[network] bridge %s ready, NAT enabled", bridgeName)
	})
	return err
}

// setupBridgeNetwork creates a veth pair, attaches one end to the bridge,
// moves the other into the container's network namespace, and configures
// the container's IP address and default route.
func setupBridgeNetwork(containerName string, pid int) (string, error) {
	if err := ensureBridge(); err != nil {
		return "", err
	}

	ip := allocateIP()
	vethHost := fmt.Sprintf("veth-%s", containerName)
	vethContainer := fmt.Sprintf("eth0")

	// Truncate veth name to 15 chars (IFNAMSIZ limit).
	if len(vethHost) > 15 {
		vethHost = vethHost[:15]
	}
	peerName := fmt.Sprintf("vp-%s", containerName)
	if len(peerName) > 15 {
		peerName = peerName[:15]
	}

	log.Printf("[network] setting up bridge for %s: %s ← %s → container (ip=%s)",
		containerName, bridgeName, vethHost, ip)

	// 1. Create veth pair.
	if err := run("ip", "link", "add", vethHost, "type", "veth", "peer", "name", peerName); err != nil {
		return "", fmt.Errorf("create veth: %w", err)
	}

	// 2. Attach host end to bridge.
	if err := run("ip", "link", "set", vethHost, "master", bridgeName); err != nil {
		return "", fmt.Errorf("attach to bridge: %w", err)
	}
	if err := run("ip", "link", "set", vethHost, "up"); err != nil {
		return "", fmt.Errorf("veth up: %w", err)
	}

	// 3. Move peer into container's network namespace.
	if err := run("ip", "link", "set", peerName, "netns", fmt.Sprintf("%d", pid)); err != nil {
		return "", fmt.Errorf("move veth to netns: %w", err)
	}

	// 4. Configure inside the container's namespace via nsenter.
	nse := func(args ...string) error {
		fullArgs := append([]string{"-t", fmt.Sprintf("%d", pid), "-n", "--"}, args...)
		return run("nsenter", fullArgs...)
	}

	// Rename peer to eth0 inside container.
	if err := nse("ip", "link", "set", peerName, "name", vethContainer); err != nil {
		return "", fmt.Errorf("rename veth: %w", err)
	}
	// Set IP address.
	if err := nse("ip", "addr", "add", ip+"/24", "dev", vethContainer); err != nil {
		return "", fmt.Errorf("set ip: %w", err)
	}
	// Bring up eth0 and lo.
	nse("ip", "link", "set", "lo", "up")
	if err := nse("ip", "link", "set", vethContainer, "up"); err != nil {
		return "", fmt.Errorf("eth0 up: %w", err)
	}
	// Default route via bridge.
	if err := nse("ip", "route", "add", "default", "via", bridgeIP); err != nil {
		return "", fmt.Errorf("default route: %w", err)
	}

	log.Printf("[network] %s: bridge network ready, ip=%s", containerName, ip)
	return ip, nil
}

// teardownBridgeNetwork removes the veth pair (container side auto-deleted).
func teardownBridgeNetwork(containerName string) {
	vethHost := fmt.Sprintf("veth-%s", containerName)
	if len(vethHost) > 15 {
		vethHost = vethHost[:15]
	}
	run("ip", "link", "del", vethHost)
}

// run executes a command and returns an error if it fails.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %v: %s", name, args, err, string(out))
	}
	return nil
}

//go:build linux

// pty.go — PTY allocation and interactive shell sessions.
//
// Opens a PTY pair (master/slave), runs nsenter+shell on the slave side,
// and streams raw bytes between the PTY master and a vsock connection.
// This enables interactive terminal access to containers.

package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

// openPTY allocates a pseudo-terminal pair and returns (master, slave, error).
// Uses posix_openpt/grantpt/unlockpt/ptsname — no external dependencies.
func openPTY() (*os.File, *os.File, error) {
	// Open the PTY master.
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	// grantpt + unlockpt via ioctl.
	// On Linux, these are no-ops with devpts mounted, but call them for correctness.
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		uintptr(syscall.TIOCGPTN), 0); errno != 0 {
		// TIOCGPTN just gets the PTY number, ignore errors
	}

	// Unlock the slave.
	var unlock int32 = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		uintptr(syscall.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("TIOCSPTLCK: %v", errno)
	}

	// Get the slave PTY name.
	var ptyNum uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(),
		uintptr(syscall.TIOCGPTN), uintptr(unsafe.Pointer(&ptyNum))); errno != 0 {
		master.Close()
		return nil, nil, fmt.Errorf("TIOCGPTN: %v", errno)
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", ptyNum)

	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open %s: %w", slavePath, err)
	}

	return master, slave, nil
}

// setWinSize sets the terminal window size on a PTY.
func setWinSize(fd uintptr, rows, cols uint16) error {
	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{rows, cols, 0, 0}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd,
		uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return errno
	}
	return nil
}

// ShellSession manages an interactive shell in a container.
type ShellSession struct {
	containerName string
	pid           int
	network       NetworkMode
	master        *os.File
	cmd           *exec.Cmd
	done          chan struct{}
}

// StartShell creates a PTY and runs an interactive shell inside the container.
func StartShell(containerName string, pid int, network NetworkMode) (*ShellSession, error) {
	master, slave, err := openPTY()
	if err != nil {
		return nil, fmt.Errorf("open pty: %w", err)
	}

	// Set initial window size (80x24 default, will be updated by client).
	setWinSize(master.Fd(), 24, 80)

	// Build nsenter command to enter the container and run a login shell.
	args := []string{
		"-t", fmt.Sprintf("%d", pid),
		"-p", "-m", "-u", "-i",
	}
	if network == NetBridge || network == NetNone {
		args = append(args, "-n")
	}
	args = append(args, "--", "/bin/sh", "-l")

	cmd := exec.Command("nsenter", args...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0, // slave fd will be fd 0 (stdin) due to cmd.Stdin
	}

	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return nil, fmt.Errorf("start shell: %w", err)
	}

	// Close slave in parent — only the child process uses it.
	slave.Close()

	session := &ShellSession{
		containerName: containerName,
		pid:           pid,
		network:       network,
		master:        master,
		cmd:           cmd,
		done:          make(chan struct{}),
	}

	// Wait for shell to exit in background.
	go func() {
		cmd.Wait()
		close(session.done)
	}()

	return session, nil
}

// StreamTo bidirectionally copies between the PTY master and the given
// ReadWriteCloser (typically a vsock connection). Blocks until the shell
// exits or the connection closes.
func (s *ShellSession) StreamTo(conn io.ReadWriteCloser) {
	done := make(chan struct{}, 2)

	// conn → PTY master (user input)
	go func() {
		io.Copy(s.master, conn)
		done <- struct{}{}
	}()

	// PTY master → conn (shell output)
	go func() {
		io.Copy(conn, s.master)
		done <- struct{}{}
	}()

	// Wait for shell to exit OR either copy to finish.
	select {
	case <-s.done: // shell process exited
	case <-done: // one direction closed
	}

	// Close both to unblock the other goroutine.
	s.master.Close()
	conn.Close()
	<-done // wait for second goroutine

	log.Printf("[shell] session ended for container %s", s.containerName)
}

// Resize updates the PTY window size.
func (s *ShellSession) Resize(rows, cols uint16) {
	setWinSize(s.master.Fd(), rows, cols)
}

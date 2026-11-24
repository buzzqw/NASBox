// Package ipc is the local control channel between the running GUI and the
// command line (and therefore file-manager integrations): a Unix socket in
// the user's runtime directory, one text command per connection.
//
// Commands: "show", "sync", "status", "versions <relative path>".
package ipc

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SocketPath is private to the user ($XDG_RUNTIME_DIR is 0700).
func SocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("nasbox-%d", os.Getuid()))
		_ = os.MkdirAll(dir, 0o700)
	}
	return filepath.Join(dir, "nasbox-go.sock")
}

// Handler answers one command; the reply is a single line.
type Handler func(command, argument string) string

// Serve listens until the returned stop function is called. A stale socket
// left by a crashed instance is replaced (the caller holds the instance lock,
// so no live server can own it).
func Serve(handler Handler) (func(), error) {
	path := SocketPath()
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handle(conn, handler)
		}
	}()
	return func() {
		_ = listener.Close()
		_ = os.Remove(path)
	}, nil
}

func handle(conn net.Conn, handler Handler) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && line == "" {
		return
	}
	command, argument, _ := strings.Cut(strings.TrimSpace(line), " ")
	reply := handler(command, argument)
	_, _ = fmt.Fprintln(conn, strings.ReplaceAll(reply, "\n", " "))
}

// ErrNotRunning means no GUI is listening.
var ErrNotRunning = errors.New("NASBox non e' in esecuzione")

// Send delivers one command to the running GUI and returns its reply.
func Send(command, argument string) (string, error) {
	conn, err := net.DialTimeout("unix", SocketPath(), 2*time.Second)
	if err != nil {
		return "", ErrNotRunning
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintln(conn, strings.TrimSpace(command+" "+argument)); err != nil {
		return "", err
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && reply == "" {
		return "", err
	}
	return strings.TrimSpace(reply), nil
}

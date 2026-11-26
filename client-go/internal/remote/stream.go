package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// streamCommandFunc is the process boundary for commands whose stdin can be
// larger than memory. stdout remains a small protocol response and is buffered.
type streamCommandFunc func(ctx context.Context, stdin io.Reader, name string, args ...string) (stdout, stderr []byte, err error)

var runStreamCommand streamCommandFunc = func(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	return out.Bytes(), errOut.Bytes(), err
}

// runStream executes an SSH command with a streaming stdin. It is intentionally
// separate from Run, whose []byte contract is convenient for metadata commands.
func (c *Client) runStream(ctx context.Context, args []string, stdin io.Reader, timeout time.Duration) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	command := c.Builder().Args(strings.Join(quoted, " "))
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, stderr, err := runStreamCommand(ctx, stdin, command[0], command[1:]...)
	if err != nil {
		return stdout, fmt.Errorf("ssh: %w: %s", err, cleanSSHStderr(string(stderr)))
	}
	return stdout, nil
}

const rateLimitSlice = 32 * 1024

type rateLimitedWriter struct {
	ctx    context.Context
	writer io.Writer
	limit  int64
}

func (w *rateLimitedWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		end := len(data)
		if end > rateLimitSlice {
			end = rateLimitSlice
		}
		if err := waitTransferLimit(w.ctx, w.limit, int64(end)); err != nil {
			return written, err
		}
		n, err := w.writer.Write(data[:end])
		written += n
		data = data[n:]
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func waitTransferLimit(ctx context.Context, bytesPerSecond int64, count int64) error {
	if bytesPerSecond <= 0 || count <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	duration := time.Duration(count * int64(time.Second) / bytesPerSecond)
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

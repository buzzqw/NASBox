package serverd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type leaseTicket struct {
	Priority int64
	Created  int64
	Device   string
	PID      int
	ID       string
}

func (c *Context) queueDir() string { return c.paths().QueueDir }

func (c *Context) pruneTickets() {
	dir := c.queueDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".ticket") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		fields := strings.Split(strings.TrimSpace(string(data)), "|")
		if len(fields) < 4 {
			os.Remove(filepath.Join(dir, entry.Name()))
			continue
		}
		pid, err := strconv.Atoi(fields[3])
		if err != nil || !processAlive(pid) {
			os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func readTicket(path string) (leaseTicket, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return leaseTicket{}, err
	}
	fields := strings.Split(strings.TrimSpace(string(data)), "|")
	if len(fields) < 4 {
		return leaseTicket{}, fmt.Errorf("ticket malformato")
	}
	priority, _ := strconv.ParseInt(fields[0], 10, 64)
	created, _ := strconv.ParseInt(fields[1], 10, 64)
	pid, _ := strconv.Atoi(fields[3])
	return leaseTicket{Priority: priority, Created: created, Device: fields[2], PID: pid}, nil
}

func (c *Context) withQueueLock(fn func() error) error {
	path := filepath.Join(c.StateDir, "transfer-queue.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return fn()
}

func (c *Context) writeOwner(device, host string, phase string, done, total int64) error {
	p := c.paths()
	payload := fmt.Sprintf("%s|%s|%d|%s|%d|%d\n", device, host, time.Now().Unix(), phase, done, total)
	tmp := p.Owner + "." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, []byte(payload), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.Owner)
}

func cmdTransferWait(ctx *Context) error {
	reader := bufio.NewReader(os.Stdin)
	fields := make([]string, 0, 5)
	for len(fields) < 5 {
		frame, err := reader.ReadString('\x00')
		if err != nil {
			return exitCode(2)
		}
		fields = append(fields, strings.TrimSuffix(frame, "\x00"))
	}
	if fields[0] != "TRANSFER_WAIT_V1" {
		return exitCode(2)
	}
	device, priorityRaw, requestID, host := fields[1], fields[2], fields[3], fields[4]
	priority, err := strconv.ParseInt(priorityRaw, 10, 64)
	if err != nil || priority < 0 || priority > 9 || len(requestID) < 8 || !tokenPattern.MatchString(requestID) {
		return exitCode(2)
	}
	if !tokenPattern.MatchString(device) {
		return exitCode(2)
	}
	if host == "" || host == "unknown" {
		host = "unknown"
	}
	dir := ctx.queueDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ticketPath := filepath.Join(dir, requestID+".ticket")
	ticketData := fmt.Sprintf("%d|%d|%s|%d\n", priority, time.Now().Unix(), device, os.Getpid())
	if err := os.WriteFile(ticketPath, []byte(ticketData), 0o600); err != nil {
		return err
	}
	defer os.Remove(ticketPath)

	p := ctx.paths()
	lock, err := os.OpenFile(p.Lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()

	granted := false
	defer func() {
		if granted {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			_ = os.Remove(p.Owner)
		}
	}()

	if err := ctx.withQueueLock(func() error {
		ctx.pruneTickets()
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		tickets := []leaseTicket{}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".ticket") {
				continue
			}
			ticket, err := readTicket(filepath.Join(dir, entry.Name()))
			if err == nil {
				tickets = append(tickets, ticket)
			}
		}
		sort.Slice(tickets, func(i, j int) bool {
			if tickets[i].Priority != tickets[j].Priority {
				return tickets[i].Priority < tickets[j].Priority
			}
			return tickets[i].Created < tickets[j].Created
		})
		if len(tickets) == 0 || tickets[0].Device != device {
			return fmt.Errorf("in coda")
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return fmt.Errorf("lock occupato")
		}
		granted = true
		return nil
	}); err != nil {
		return err
	}

	if err := ctx.writeOwner(device, host, "", 0, 0); err != nil {
		return err
	}
	fmt.Println("NASBOX_LOCKED")

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	for {
		select {
		case <-signals:
			return exitCode(143)
		default:
		}
		frame, err := reader.ReadString('\x00')
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		parts := strings.Split(strings.TrimSuffix(frame, "\x00"), "\x00")
		if len(parts) == 4 && parts[0] == "ACTIVITY_V1" {
			done, _ := strconv.ParseInt(parts[2], 10, 64)
			total, _ := strconv.ParseInt(parts[3], 10, 64)
			_ = ctx.writeOwner(device, host, parts[1], done, total)
		}
	}
}

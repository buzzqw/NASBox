package serverd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (c *Context) daemonLockPath() string { return filepath.Join(c.StateDir, "daemon.lock") }
func (c *Context) pidPath() string        { return filepath.Join(c.StateDir, "daemon.pid") }

func (c *Context) daemonRunning() (int, bool) {
	data, err := os.ReadFile(c.pidPath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	process, err := os.FindProcess(pid)
	if err != nil || process.Signal(syscall.Signal(0)) != nil {
		return pid, false
	}
	cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return pid, true
	}
	text := strings.ReplaceAll(string(cmdline), "\x00", " ")
	binary, _ := os.Executable()
	return pid, strings.Contains(text, binary) && strings.Contains(text, "--run-foreground")
}

func cmdDaemonForeground(ctx *Context) error {
	file, err := os.OpenFile(ctx.daemonLockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(os.Stderr, "Avvio ignorato: un'altra istanza del demone è già attiva.")
		return nil
	}
	if err := os.WriteFile(ctx.pidPath(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(ctx.pidPath())

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	interval := 60
	if raw := strings.Fields(ctx.Config["CHECK_INTERVAL_MINUTES"]); len(raw) > 0 {
		if n, err := strconv.Atoi(raw[0]); err == nil && n > 0 {
			interval = n
		}
	}
	fmt.Printf("sync-daemon-server v%s avviato in foreground (PID %d), intervallo interno: %d min\n", version, os.Getpid(), interval)
	for {
		if err := cmdRunOnce(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "passo di manutenzione fallito:", err)
		}
		select {
		case <-signals:
			fmt.Println("Demone arrestato.")
			return nil
		case <-time.After(time.Duration(interval) * time.Minute):
		}
	}
}

func cmdStart(ctx *Context) error {
	if pid, running := ctx.daemonRunning(); running {
		fmt.Printf("Il demone è già in esecuzione (PID %d).\n", pid)
		return nil
	}
	os.Remove(ctx.pidPath())
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(ctx.StateDir, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(binary, "-c", ctx.Options.ConfigPath, "--run-foreground")
	command.Stdout = logFile
	command.Stderr = logFile
	command.Stdin = nil
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		logFile.Close()
		return err
	}
	logFile.Close()
	pid := command.Process.Pid
	_ = command.Process.Release()
	fmt.Printf("Demone avviato (PID %d).\n", pid)
	return nil
}

func cmdStop(ctx *Context) error {
	pid, running := ctx.daemonRunning()
	if !running {
		fmt.Println("Demone non in esecuzione.")
		os.Remove(ctx.pidPath())
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		if _, still := ctx.daemonRunning(); !still {
			fmt.Println("Demone arrestato.")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("il demone non si e' arrestato (PID %d)", pid)
}

func cmdRestart(ctx *Context) error {
	if err := cmdStop(ctx); err != nil {
		return err
	}
	return cmdStart(ctx)
}

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Supervisor manages a single xray process.
type Supervisor struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	binPath string
	cfgPath string
	logPath string
	running bool
	gen     int // guards against stale watchers clearing a newer process
}

func NewSupervisor(binPath, cfgPath, logPath string) *Supervisor {
	return &Supervisor{
		binPath: binPath,
		cfgPath: cfgPath,
		logPath: logPath,
	}
}

func FindXray() (string, error) {
	if p, err := exec.LookPath("xray"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/xray", "/usr/local/bin/xray", "/usr/bin/xray"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("xray binary not found in PATH")
}

// KillOrphanListeners finds and kills any orphaned processes bound to our ports
// before starting a new xray instance, ensuring clean port ownership.
func KillOrphanListeners(ports ...int) {
	for _, port := range ports {
		if port <= 0 {
			continue
		}
		// Use lsof -tiTCP:<port> -sTCP:LISTEN to find the specific PID listening on the port
		out, err := exec.Command("lsof", "-tiTCP:"+fmt.Sprint(port), "-sTCP:LISTEN").Output()
		if err == nil && len(out) > 0 {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				var pid int
				if _, err := fmt.Sscan(line, &pid); err == nil && pid > 0 && pid != os.Getpid() {
					// Verify it's actually an xray process before killing
					comm, _ := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "comm=").Output()
					if strings.Contains(string(comm), "xray") {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			}
		}
	}
}

// Start launches xray. Returns an error if already running or launch failed.
// The mutex is never held while sleeping or waiting on the child.
func (s *Supervisor) Start() error {
	s.mu.Lock()
	if s.running {
		pid := 0
		if s.cmd != nil && s.cmd.Process != nil {
			pid = s.cmd.Process.Pid
		}
		s.mu.Unlock()
		return fmt.Errorf("xray already running (pid %d)", pid)
	}

	// Always clean up any stale orphaned xray listeners before binding
	KillOrphanListeners(18080, 18081, 19082)

	logFile, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("open log: %w", err)
	}

	cmd := exec.Command(s.binPath, "run", "-c", s.cfgPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		s.mu.Unlock()
		return fmt.Errorf("start xray: %w", err)
	}

	s.gen++
	gen := s.gen
	s.cmd = cmd
	s.running = true
	exitCh := make(chan error, 1)
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	s.mu.Unlock()

	go func() {
		err := cmd.Wait()
		logFile.Close()
		exitCh <- err
		close(doneCh)
	}()

	// Watcher clears `running` when the process dies on its own, but only if
	// no newer Start() has replaced it in the meantime.
	go func() {
		select {
		case <-exitCh:
		case <-stopCh:
			return
		}
		s.mu.Lock()
		if s.gen == gen {
			s.running = false
		}
		s.mu.Unlock()
	}()

	// Brief grace period to catch instant config failures, without holding the lock.
	select {
	case <-doneCh:
		s.mu.Lock()
		if s.gen == gen {
			s.running = false
		}
		s.mu.Unlock()
		return fmt.Errorf("xray exited immediately; check log: %s", s.logPath)
	case <-time.After(200 * time.Millisecond):
	}
	return nil
}

// Stop terminates the xray process group.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	if !s.running || s.cmd == nil || s.cmd.Process == nil {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	s.gen++ // invalidate the watcher
	cmd := s.cmd
	s.mu.Unlock()

	pgid := -cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		<-done
	}
	return nil
}

func (s *Supervisor) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Supervisor) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}

// ConfigPath returns the path to the generated xray config.
func (s *Supervisor) ConfigPath() string { return s.cfgPath }

func EnsureDir(path string) error {
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

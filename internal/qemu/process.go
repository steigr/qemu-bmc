package qemu

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ProcessManager controls the lifecycle of a QEMU process.
type ProcessManager interface {
	Start(bootTarget string) error
	Stop(timeout time.Duration) error
	Kill() error
	IsRunning() bool
	WaitForExit(timeout time.Duration) error
	ExitCh() <-chan struct{}
}

// CommandFactory creates exec.Cmd instances. Allows test injection.
type CommandFactory func(binary string, args []string) *exec.Cmd

// DefaultCommandFactory creates a standard exec.Cmd.
func DefaultCommandFactory(binary string, args []string) *exec.Cmd {
	return exec.Command(binary, args...)
}

type processManager struct {
	binary     string
	baseArgs   []string
	cmdFactory CommandFactory
	cmd        *exec.Cmd
	running    bool
	exitCh     chan struct{}
	mu         sync.RWMutex
}

// NewProcessManager creates a ProcessManager for the given QEMU binary and base arguments.
func NewProcessManager(binary string, baseArgs []string, factory CommandFactory) ProcessManager {
	return &processManager{
		binary:     binary,
		baseArgs:   baseArgs,
		cmdFactory: factory,
		exitCh:     make(chan struct{}),
	}
}

func (p *processManager) Start(bootTarget string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return nil // already running, no-op
	}

	args := ApplyBootOverride(p.baseArgs, bootTarget)
	p.cmd = p.cmdFactory(p.binary, args)
	p.cmd.Stdout = os.Stdout
	p.cmd.Stderr = os.Stderr
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("starting QEMU process: %w", err)
	}

	p.running = true
	p.exitCh = make(chan struct{})

	go p.monitor()
	return nil
}

func (p *processManager) monitor() {
	p.cmd.Wait()
	p.mu.Lock()
	p.running = false
	ch := p.exitCh
	p.mu.Unlock()
	close(ch)
}

func (p *processManager) Stop(timeout time.Duration) error {
	p.mu.RLock()
	if !p.running {
		p.mu.RUnlock()
		return nil
	}
	cmd := p.cmd
	p.mu.RUnlock()

	if cmd.Process == nil {
		return nil
	}

	// Send SIGTERM
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending SIGTERM: %w", err)
	}

	// Wait with timeout
	if err := p.WaitForExit(timeout); err != nil {
		// Timeout — force kill
		return p.Kill()
	}
	return nil
}

func (p *processManager) Kill() error {
	p.mu.RLock()
	if !p.running {
		p.mu.RUnlock()
		return nil
	}
	cmd := p.cmd
	p.mu.RUnlock()

	if cmd.Process == nil {
		return nil
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("sending SIGKILL: %w", err)
	}
	return nil
}

func (p *processManager) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

func (p *processManager) WaitForExit(timeout time.Duration) error {
	p.mu.RLock()
	ch := p.exitCh
	p.mu.RUnlock()

	select {
	case <-ch:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for process exit after %s", timeout)
	}
}

func (p *processManager) ExitCh() <-chan struct{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exitCh
}

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const governancePIDEnv = "QEMU_BMC_GOVERNANCE_PID"

type governanceOptions struct {
	resetSignal  syscall.Signal
	varsFile     string
	varsTemplate string
	childArgs    []string
}

func runGovernance(args []string) error {
	opts, err := parseGovernanceOptions(args)
	if err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	for {
		childCmd := append([]string{"--"}, opts.childArgs...)
		cmd := exec.Command(self, childCmd...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d", governancePIDEnv, os.Getpid()))

		// Detach child into new process group so it's independent of parent signals
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
			Pgid:    0,
		}

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start managed qemu-bmc: %w", err)
		}
		log.Printf("governance: started managed qemu-bmc pid=%d", cmd.Process.Pid)

		waitCh := make(chan error, 1)
		go func() {
			waitCh <- cmd.Wait()
		}()

		sigCh := make(chan os.Signal, 2)
		signal.Notify(sigCh, opts.resetSignal, syscall.SIGINT, syscall.SIGTERM)

		restartRequested := false
		exitRequested := false

		for !restartRequested && !exitRequested {
			select {
			case err := <-waitCh:
				signal.Stop(sigCh)
				if restartRequested || exitRequested {
					break
				}
				if err != nil {
					return fmt.Errorf("managed qemu-bmc exited: %w", err)
				}
				return nil
			case sig := <-sigCh:
				switch sig {
				case opts.resetSignal:
					restartRequested = true
					log.Printf("governance: received reset signal %v", sig)
				default:
					exitRequested = true
					log.Printf("governance: received shutdown signal %v", sig)
				}
			}
		}

		signal.Stop(sigCh)

		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		if err := waitWithTimeout(waitCh, 20*time.Second); err != nil {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGKILL)
			}
			_ = waitWithTimeout(waitCh, 5*time.Second)
		}

		if exitRequested {
			return nil
		}

		if opts.varsFile != "" && opts.varsTemplate != "" {
			if err := resetVarsFile(opts.varsFile, opts.varsTemplate); err != nil {
				return err
			}
			log.Printf("governance: reset vars file %s from template %s", opts.varsFile, opts.varsTemplate)
		}
	}
}

func parseGovernanceOptions(args []string) (*governanceOptions, error) {
	fs := flag.NewFlagSet("governance", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	resetSignalName := fs.String("reset-signal", "USR2", "signal used to reset vars and restart managed qemu-bmc")
	varsFile := fs.String("vars-file", "", "UEFI vars file path to replace on reset")
	varsTemplate := fs.String("vars-template", "", "UEFI vars template copied to vars-file on reset")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	managedArgs := fs.Args()
	if len(managedArgs) == 0 {
		return nil, fmt.Errorf("governance requires managed command arguments, e.g. governance -- <qemu args>")
	}

	if managedArgs[0] == "--" {
		managedArgs = managedArgs[1:]
	}
	if len(managedArgs) == 0 {
		return nil, fmt.Errorf("governance requires qemu args after --")
	}

	sig, err := parseSignalName(*resetSignalName)
	if err != nil {
		return nil, err
	}

	return &governanceOptions{
		resetSignal:  sig,
		varsFile:     *varsFile,
		varsTemplate: *varsTemplate,
		childArgs:    managedArgs,
	}, nil
}

func parseSignalName(v string) (syscall.Signal, error) {
	s := strings.ToUpper(strings.TrimSpace(v))
	s = strings.TrimPrefix(s, "SIG")
	switch s {
	case "USR2":
		return syscall.SIGUSR2, nil
	case "USR1":
		return syscall.SIGUSR1, nil
	case "WINCH":
		return syscall.SIGWINCH, nil
	default:
		return 0, fmt.Errorf("unsupported reset signal %q", v)
	}
}

func resetVarsFile(varsFile, varsTemplate string) error {
	if varsFile == "" || varsTemplate == "" {
		return nil
	}

	data, err := os.ReadFile(varsTemplate)
	if err != nil {
		return fmt.Errorf("read vars template %s: %w", varsTemplate, err)
	}

	dir := filepath.Dir(varsFile)
	tmp, err := os.CreateTemp(dir, "qemu-bmc-vars-*.fd")
	if err != nil {
		return fmt.Errorf("create temp vars file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp vars file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp vars file: %w", err)
	}

	if err := os.Rename(tmpPath, varsFile); err != nil {
		return fmt.Errorf("replace vars file: %w", err)
	}
	return nil
}

func waitWithTimeout(waitCh <-chan error, timeout time.Duration) error {
	select {
	case err := <-waitCh:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("wait timed out after %s", timeout)
	}
}


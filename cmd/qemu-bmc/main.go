package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/steigr/qemu-bmc/internal/bmc"
	"github.com/steigr/qemu-bmc/internal/config"
	"github.com/steigr/qemu-bmc/internal/ipmi"
	"github.com/steigr/qemu-bmc/internal/machine"
	"github.com/steigr/qemu-bmc/internal/qemu"
	"github.com/steigr/qemu-bmc/internal/qmp"
	"github.com/steigr/qemu-bmc/internal/redfish"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-v" {
		fmt.Println(version)
		os.Exit(0)
	}

	if len(os.Args) > 1 && os.Args[1] == "governance" {
		if err := runGovernance(os.Args[2:]); err != nil {
			log.Fatalf("governance failed: %v", err)
		}
		return
	}

	if err := runServer(os.Args[1:]); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("qemu-bmc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("qemu-bmc %s starting...", version)

	cfg := config.Load()
	qemuArgs := fs.Args()

	if len(qemuArgs) == 0 {
		return fmt.Errorf("usage: qemu-bmc [flags] -- <qemu-system arguments>\n\nQEMU arguments are required. Example:\n  qemu-bmc -- -m 2048 -smp 2 -nographic")
	}

	// Strip leading "--" separator (passed by governance or user)
	if qemuArgs[0] == "--" {
		qemuArgs = qemuArgs[1:]
	}
	if len(qemuArgs) == 0 {
		return fmt.Errorf("no QEMU arguments provided after --")
	}

	cmdArgs, err := qemu.BuildCommandLine(qemuArgs, qemu.BuildOptions{
		QMPSocketPath: cfg.QMPSocket,
		SerialAddr:    cfg.SerialAddr,
	})
	if err != nil {
		return fmt.Errorf("invalid QEMU arguments: %w", err)
	}

	qmpClient := qmp.NewDisconnectedClient(cfg.QMPSocket)
	defer qmpClient.Close()
	pm := qemu.NewProcessManager(cfg.QEMUBinary, cmdArgs, qemu.DefaultCommandFactory)
	m := machine.New(qmpClient, pm)

	if cfg.PowerOnAtStart {
		log.Printf("Starting QEMU: %s %v", cfg.QEMUBinary, cmdArgs)
		if err := m.Reset("On"); err != nil {
			return fmt.Errorf("failed to start QEMU: %w", err)
		}
	} else {
		log.Printf("POWER_ON_AT_START=false: QEMU will not start until powered on via IPMI/Redfish")
	}

	// Create BMC state
	bmcState := bmc.NewState(cfg.IPMIUser, cfg.IPMIPass)

	// Start VM IPMI server (only if configured)
	if cfg.VMIPMIAddr != "" {
		vmServer := ipmi.NewVMServer(m, bmcState)
		go func() {
			log.Printf("Starting VM IPMI server on %s", cfg.VMIPMIAddr)
			if err := vmServer.ListenAndServe(cfg.VMIPMIAddr); err != nil {
				log.Printf("VM IPMI server error: %v", err)
			}
		}()
	}

	// Start IPMI server
	ipmiServer := ipmi.NewServer(m, bmcState, cfg.IPMIUser, cfg.IPMIPass)
	go func() {
		addr := fmt.Sprintf(":%s", cfg.IPMIPort)
		log.Printf("Starting IPMI server on %s", addr)
		if err := ipmiServer.ListenAndServe(addr); err != nil {
			log.Printf("IPMI server error: %v", err)
		}
	}()

	// Start Redfish server
	redfishServer := redfish.NewServer(m, cfg.IPMIUser, cfg.IPMIPass, cfg.VNCAddr)
	addr := net.JoinHostPort(cfg.RedfishAddr, cfg.RedfishPort)
	log.Printf("Starting Redfish server on %s", addr)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: redfishServer,
	}

	go func() {
		if cfg.UseTLS {
			if err := httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey); err != nil && err != http.ErrServerClosed {
				log.Printf("Redfish server error: %v", err)
			}
		} else {
			if (cfg.TLSCert != "" && cfg.TLSKey == "") || (cfg.TLSCert == "" && cfg.TLSKey != "") {
				log.Printf("TLS disabled: both TLS_CERT and TLS_KEY are required")
			}
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("Redfish server error: %v", err)
			}
		}
	}()

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %s, shutting down...", sig)

	// Shutdown QEMU process
	log.Println("Stopping QEMU process...")
	if err := m.Reset("ForceOff"); err != nil {
		log.Printf("Error during QEMU shutdown: %v", err)
	}
	// Give process time to exit
	time.Sleep(500 * time.Millisecond)

	return nil
}

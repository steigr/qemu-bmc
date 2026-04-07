// ipmi-test-server starts a minimal IPMI server for manual/integration testing
// without requiring a running QEMU instance.
//
// Usage:
//
//	go run ./cmd/ipmi-test-server [-port 6234]
package main

import (
	"flag"
	"fmt"
	"log"
	"net"

	"github.com/steigr/qemu-bmc/internal/bmc"
	"github.com/steigr/qemu-bmc/internal/ipmi"
	"github.com/steigr/qemu-bmc/internal/machine"
)

// stubMachine simulates a powered-on VM for testing purposes.
type stubMachine struct{}

func (s *stubMachine) GetPowerState() (machine.PowerState, error) { return machine.PowerOn, nil }
func (s *stubMachine) Reset(_ string) error                       { return nil }
func (s *stubMachine) GetBootOverride() machine.BootOverride {
	return machine.BootOverride{Enabled: "Disabled", Target: "None", Mode: "UEFI"}
}
func (s *stubMachine) SetBootOverride(override machine.BootOverride) error { return nil }

func main() {
	port := flag.Int("port", 6234, "UDP port to listen on")
	flag.Parse()

	state := bmc.NewState("admin", "password")
	server := ipmi.NewServer(&stubMachine{}, state, "admin", "password")

	addr := fmt.Sprintf(":%d", *port)
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		log.Fatalf("Listen %s: %v", addr, err)
	}
	log.Printf("IPMI test server listening on %s (user=admin pass=password)", addr)
	if err := server.Serve(conn); err != nil {
		log.Fatalf("Serve: %v", err)
	}
}

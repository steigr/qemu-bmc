// Package bmctest provides reusable test helpers for e2e testing of qemu-bmc.
//
// Instead of building and running qemu-bmc as a subprocess, this package
// wires up the internal components (QMP client, process manager, machine,
// Redfish server, IPMI server) directly in the test process.
//
// Other projects can import this package to spin up a qemu-bmc instance
// managing a real QEMU VM and interact with it via Redfish and IPMI APIs.
//
// # Quick Start
//
//	func TestMyVMWorkflow(t *testing.T) {
//	    bmc := bmctest.New(t, bmctest.Config{
//	        QEMUBin:  "qemu-system-x86_64",
//	        QEMUArgs: []string{"-accel", "tcg", "-cpu", "qemu64", "-machine", "q35", "-m", "512", "-smp", "1", "-nographic"},
//	    })
//	    defer bmc.Cleanup()
//	    bmc.WaitReady(60 * time.Second)
//	    state := bmc.GetPowerState()
//	    // ... run Redfish / IPMI assertions ...
//	}
//
// # Requirements
//
//   - QEMU system emulator installed (e.g. qemu-system-x86_64).
//   - For UEFI tests: OVMF/AAVMF firmware files.
package bmctest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steigr/qemu-bmc/internal/bmc"
	"github.com/steigr/qemu-bmc/internal/ipmi"
	"github.com/steigr/qemu-bmc/internal/machine"
	"github.com/steigr/qemu-bmc/internal/qemu"
	"github.com/steigr/qemu-bmc/internal/qmp"
	"github.com/steigr/qemu-bmc/internal/redfish"
)

// Config describes the qemu-bmc + QEMU configuration for a test.
type Config struct {
	// QEMUBin is the QEMU system binary (e.g. "qemu-system-x86_64").
	QEMUBin string

	// QEMUArgs are the QEMU command-line arguments passed after "--".
	QEMUArgs []string

	// IPMIUser and IPMIPass are the credentials for Redfish Basic Auth
	// and IPMI authentication. Default to "admin" / "password".
	IPMIUser string
	IPMIPass string

	// PowerOnAtStart controls whether the VM starts automatically.
	// Defaults to true.
	PowerOnAtStart *bool

	// SetupFirmware is an optional callback that can modify QEMUArgs
	// (e.g. to copy UEFI NVRAM templates into a temp directory).
	// It receives the temp directory and the original args, and returns
	// the modified args.
	SetupFirmware func(t *testing.T, tmpDir string, args []string) []string
}

// BMC represents a running qemu-bmc instance for testing.
type BMC struct {
	t *testing.T

	// RedfishBase is the base URL for Redfish API calls (e.g. "http://127.0.0.1:12345").
	RedfishBase string

	// RedfishPort is the port the Redfish HTTP server is listening on.
	RedfishPort string

	// IPMIPort is the port the IPMI UDP server is listening on.
	IPMIPort string

	// User and Pass are the configured credentials.
	User string
	Pass string

	// Machine provides direct access to the machine interface for
	// advanced test scenarios that go beyond the Redfish API.
	Machine redfish.MachineInterface

	qmpClient  qmp.Client
	pm         qemu.ProcessManager
	httpServer *http.Server
	ipmiServer *ipmi.Server
	outputBuf  *syncBuffer
	tmpDir     string
	cleanupMu  sync.Mutex
	cleanedUp  bool
}

// New creates and starts a new qemu-bmc instance for testing.
// All components (QMP client, QEMU process manager, Redfish server,
// IPMI server) run in-process — no subprocess compilation needed.
//
// Call Cleanup() (or use defer bmc.Cleanup()) to stop the QEMU process
// and shut down servers.
func New(t *testing.T, cfg Config) *BMC {
	t.Helper()

	if cfg.QEMUBin == "" {
		t.Fatal("bmctest.Config.QEMUBin is required")
	}
	if _, err := exec.LookPath(cfg.QEMUBin); err != nil {
		t.Skipf("skipping: %s not in PATH", cfg.QEMUBin)
	}

	if cfg.IPMIUser == "" {
		cfg.IPMIUser = "admin"
	}
	if cfg.IPMIPass == "" {
		cfg.IPMIPass = "password"
	}
	powerOn := true
	if cfg.PowerOnAtStart != nil {
		powerOn = *cfg.PowerOnAtStart
	}

	tmpDir := t.TempDir()
	qemuArgs := cfg.QEMUArgs
	if cfg.SetupFirmware != nil {
		qemuArgs = cfg.SetupFirmware(t, tmpDir, qemuArgs)
	}

	// QMP socket in temp dir
	qmpSock := filepath.Join(tmpDir, "qmp.sock")

	// Build QEMU command line (validates args, applies defaults, injects QMP)
	cmdArgs, err := qemu.BuildCommandLine(qemuArgs, qemu.BuildOptions{
		QMPSocketPath: qmpSock,
		SerialAddr:    "", // nographic handles serial
	})
	if err != nil {
		t.Fatalf("building QEMU command line: %v", err)
	}

	// Capture QEMU stdout/stderr for output inspection
	outputBuf := &syncBuffer{}

	// Create a command factory that captures output
	cmdFactory := func(binary string, args []string) *exec.Cmd {
		cmd := exec.Command(binary, args...)
		cmd.Stdout = outputBuf
		cmd.Stderr = outputBuf
		return cmd
	}

	// Create core components
	qmpClient := qmp.NewDisconnectedClient(qmpSock)
	pm := qemu.NewProcessManager(cfg.QEMUBin, cmdArgs, cmdFactory)
	m := machine.New(qmpClient, pm)

	// Create BMC state
	bmcState := bmc.NewState(cfg.IPMIUser, cfg.IPMIPass)

	// Start IPMI server on a free port
	ipmiPort := freePort(t)
	ipmiServer := ipmi.NewServer(m, bmcState, cfg.IPMIUser, cfg.IPMIPass)
	go func() {
		addr := fmt.Sprintf(":%s", ipmiPort)
		if err := ipmiServer.ListenAndServe(addr); err != nil {
			// Ignore errors from normal shutdown
			if !strings.Contains(err.Error(), "use of closed") {
				t.Logf("IPMI server error: %v", err)
			}
		}
	}()

	// Start Redfish server on a free port
	redfishPort := freePort(t)
	redfishServer := redfish.NewServer(m, cfg.IPMIUser, cfg.IPMIPass, "")
	addr := net.JoinHostPort("127.0.0.1", redfishPort)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: redfishServer,
	}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Logf("Redfish server error: %v", err)
		}
	}()

	// Optionally power on the VM
	if powerOn {
		t.Logf("Starting QEMU: %s (args count: %d)", cfg.QEMUBin, len(cmdArgs))
		if err := m.Reset("On"); err != nil {
			t.Fatalf("failed to start QEMU: %v", err)
		}
	}

	b := &BMC{
		t:           t,
		RedfishBase: fmt.Sprintf("http://127.0.0.1:%s", redfishPort),
		RedfishPort: redfishPort,
		IPMIPort:    ipmiPort,
		User:        cfg.IPMIUser,
		Pass:        cfg.IPMIPass,
		Machine:     m,
		qmpClient:   qmpClient,
		pm:          pm,
		httpServer:  httpServer,
		ipmiServer:  ipmiServer,
		outputBuf:   outputBuf,
		tmpDir:      tmpDir,
	}

	t.Logf("qemu-bmc started in-process (redfish=%s, ipmi=%s)", redfishPort, ipmiPort)
	return b
}

// Cleanup stops the QEMU process, shuts down servers, and releases resources.
// Safe to call multiple times.
func (b *BMC) Cleanup() {
	b.cleanupMu.Lock()
	defer b.cleanupMu.Unlock()
	if b.cleanedUp {
		return
	}
	b.cleanedUp = true

	b.t.Helper()
	b.t.Log("Cleaning up qemu-bmc test instance")

	// Stop QEMU process
	if err := b.pm.Stop(10 * time.Second); err != nil {
		b.t.Logf("Error stopping QEMU: %v, force killing", err)
		_ = b.pm.Kill()
	}

	// Close QMP client
	_ = b.qmpClient.Close()

	// Stop IPMI server
	_ = b.ipmiServer.Close()

	// Stop HTTP server
	_ = b.httpServer.Close()
}

// WaitReady blocks until the Redfish service root responds with 200 OK,
// or fails the test if the timeout expires.
func (b *BMC) WaitReady(timeout time.Duration) {
	b.t.Helper()
	b.t.Log("Waiting for Redfish to become ready...")
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("GET", b.RedfishBase+"/redfish/v1", nil)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		req.SetBasicAuth(b.User, b.Pass)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				b.t.Log("Redfish is ready")
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	b.t.Fatalf("Redfish did not become ready within %s", timeout)
}

// Output returns the captured QEMU stdout+stderr output so far.
func (b *BMC) Output() string {
	return b.outputBuf.String()
}

// OutputTail returns the last n bytes of captured output.
func (b *BMC) OutputTail(n int) string {
	return b.outputBuf.Tail(n)
}

// WaitForOutput polls the output buffer until the given substring appears,
// or the deadline expires. Returns true if found.
func (b *BMC) WaitForOutput(substr string, timeout time.Duration) bool {
	b.t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return false
		case <-ticker.C:
			if strings.Contains(b.outputBuf.String(), substr) {
				return true
			}
		}
	}
}

// --- Redfish API Helpers ---

// GetServiceRoot fetches the Redfish service root.
func (b *BMC) GetServiceRoot() map[string]interface{} {
	b.t.Helper()
	return b.redfishGet("/redfish/v1")
}

// GetPowerState returns the current power state ("On" or "Off").
func (b *BMC) GetPowerState() string {
	b.t.Helper()
	sys := b.redfishGet("/redfish/v1/Systems/1")
	state, _ := sys["PowerState"].(string)
	return state
}

// GetSystem returns the full ComputerSystem resource.
func (b *BMC) GetSystem() map[string]interface{} {
	b.t.Helper()
	return b.redfishGet("/redfish/v1/Systems/1")
}

// GetBootOverride returns the current boot override settings.
func (b *BMC) GetBootOverride() (enabled, target, mode string) {
	b.t.Helper()
	sys := b.redfishGet("/redfish/v1/Systems/1")
	boot, ok := sys["Boot"].(map[string]interface{})
	if !ok {
		b.t.Fatal("Boot field missing from system response")
	}
	enabled, _ = boot["BootSourceOverrideEnabled"].(string)
	target, _ = boot["BootSourceOverrideTarget"].(string)
	mode, _ = boot["BootSourceOverrideMode"].(string)
	return
}

// SetBootOverride sets the boot source override via PATCH.
func (b *BMC) SetBootOverride(enabled, target, mode string) {
	b.t.Helper()
	body := fmt.Sprintf(`{
		"Boot": {
			"BootSourceOverrideEnabled": %q,
			"BootSourceOverrideTarget": %q,
			"BootSourceOverrideMode": %q
		}
	}`, enabled, target, mode)
	b.redfishRequest("PATCH", "/redfish/v1/Systems/1", body, http.StatusOK)
}

// ResetSystem sends a ComputerSystem.Reset action.
func (b *BMC) ResetSystem(resetType string) {
	b.t.Helper()
	body := fmt.Sprintf(`{"ResetType": %q}`, resetType)
	b.redfishRequest("POST",
		"/redfish/v1/Systems/1/Actions/ComputerSystem.Reset",
		body, http.StatusNoContent)
}

// InsertMedia inserts virtual media via the Redfish VirtualMedia API.
func (b *BMC) InsertMedia(imageURL string) {
	b.t.Helper()
	body := fmt.Sprintf(`{"Image": %q, "Inserted": true}`, imageURL)
	b.redfishRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
		body, http.StatusOK)
}

// EjectMedia ejects the currently inserted virtual media.
func (b *BMC) EjectMedia() {
	b.t.Helper()
	b.redfishRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia",
		"{}", http.StatusOK)
}

// GetVirtualMedia returns the VirtualMedia/CD1 resource.
func (b *BMC) GetVirtualMedia() map[string]interface{} {
	b.t.Helper()
	return b.redfishGet("/redfish/v1/Managers/1/VirtualMedia/CD1")
}

// GetManager returns the Manager/1 resource.
func (b *BMC) GetManager() map[string]interface{} {
	b.t.Helper()
	return b.redfishGet("/redfish/v1/Managers/1")
}

// GetChassis returns the Chassis/1 resource.
func (b *BMC) GetChassis() map[string]interface{} {
	b.t.Helper()
	return b.redfishGet("/redfish/v1/Chassis/1")
}

// --- Low-Level Helpers ---

func (b *BMC) redfishGet(path string) map[string]interface{} {
	b.t.Helper()
	req, err := http.NewRequest("GET", b.RedfishBase+path, nil)
	if err != nil {
		b.t.Fatalf("creating request: %v", err)
	}
	req.SetBasicAuth(b.User, b.Pass)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		b.t.Fatalf("GET %s failed: %v", path, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("GET %s returned %d: %s", path, resp.StatusCode, body)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		b.t.Fatalf("parsing JSON from %s: %v\nBody: %s", path, err, body)
	}
	return result
}

func (b *BMC) redfishRequest(method, path, body string, expectedStatus int) []byte {
	b.t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, b.RedfishBase+path, bodyReader)
	if err != nil {
		b.t.Fatalf("creating %s request: %v", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(b.User, b.Pass)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != expectedStatus {
		b.t.Fatalf("%s %s returned %d (expected %d): %s", method, path, resp.StatusCode, expectedStatus, respBody)
	}
	return respBody
}

// RedfishRequestRaw performs an authenticated request and returns the raw http.Response.
// The caller is responsible for closing resp.Body.
func (b *BMC) RedfishRequestRaw(method, path, body string) *http.Response {
	b.t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, b.RedfishBase+path, bodyReader)
	if err != nil {
		b.t.Fatalf("creating %s request: %v", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(b.User, b.Pass)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return resp
}

// --- Utility Functions ---


// FreePort returns a free TCP port on localhost as a string.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return fmt.Sprintf("%d", port)
}

// FindFirmware searches a list of "code:vars" path pairs and returns
// the first pair where both files exist. Returns empty strings if none found.
func FindFirmware(paths []string) (code string, vars string) {
	for _, pair := range paths {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if _, err := os.Stat(parts[0]); err == nil {
			if _, err := os.Stat(parts[1]); err == nil {
				return parts[0], parts[1]
			}
		}
	}
	return "", ""
}

// SetupUEFIVars copies a UEFI NVRAM vars template to tmpDir and replaces
// the placeholder "UEFI_VARS_PLACEHOLDER" in the args with the actual path.
// This is a common SetupFirmware callback.
func SetupUEFIVars(varsTemplate string) func(t *testing.T, tmpDir string, args []string) []string {
	return func(t *testing.T, tmpDir string, args []string) []string {
		t.Helper()
		liveVars := filepath.Join(tmpDir, "uefi-vars.fd")
		data, err := os.ReadFile(varsTemplate)
		if err != nil {
			t.Fatalf("reading UEFI vars template %s: %v", varsTemplate, err)
		}
		if err := os.WriteFile(liveVars, data, 0o644); err != nil {
			t.Fatalf("writing live vars: %v", err)
		}
		result := make([]string, len(args))
		for i, a := range args {
			if a == "UEFI_VARS_PLACEHOLDER" {
				a = fmt.Sprintf("if=pflash,format=raw,file=%s", liveVars)
			}
			result[i] = a
		}
		return result
	}
}

// BoolPtr returns a pointer to a bool value. Useful for Config.PowerOnAtStart.
func BoolPtr(v bool) *bool {
	return &v
}

// --- Firmware Path Constants ---

// X86_64UEFIPaths lists common locations for x86_64 UEFI firmware files.
var X86_64UEFIPaths = []string{
	"/opt/homebrew/share/qemu/edk2-x86_64-code.fd:/opt/homebrew/share/qemu/edk2-i386-vars.fd",
	"/usr/local/share/qemu/edk2-x86_64-code.fd:/usr/local/share/qemu/edk2-i386-vars.fd",
	"/usr/share/OVMF/OVMF_CODE.fd:/usr/share/OVMF/OVMF_VARS.fd",
	"/usr/share/OVMF/OVMF_CODE_4M.fd:/usr/share/OVMF/OVMF_VARS_4M.fd",
	"/usr/share/edk2/ovmf/OVMF_CODE.fd:/usr/share/edk2/ovmf/OVMF_VARS.fd",
	"/usr/share/ovmf/x64/OVMF_CODE.fd:/usr/share/ovmf/x64/OVMF_VARS.fd",
}

// AArch64UEFIPaths lists common locations for aarch64 UEFI firmware files.
var AArch64UEFIPaths = []string{
	"/opt/homebrew/share/qemu/edk2-aarch64-code.fd:/opt/homebrew/share/qemu/edk2-arm-vars.fd",
	"/usr/local/share/qemu/edk2-aarch64-code.fd:/usr/local/share/qemu/edk2-arm-vars.fd",
	"/usr/share/AAVMF/AAVMF_CODE.fd:/usr/share/AAVMF/AAVMF_VARS.fd",
	"/usr/share/qemu-efi-aarch64/AAVMF_CODE.fd:/usr/share/qemu-efi-aarch64/AAVMF_VARS.fd",
	"/usr/share/edk2/aarch64/QEMU_EFI-pflash.raw:/usr/share/edk2/aarch64/vars-template-pflash.raw",
}

// --- Internal Types ---

// syncBuffer is a goroutine-safe bytes.Buffer for capturing output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Tail(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}



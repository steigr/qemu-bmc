package e2e

import (
	"bufio"
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
	"syscall"
	"testing"
	"time"
)

// bootCDROMTestConfig describes a VM configuration for a boot-from-CDROM test.
type bootCDROMTestConfig struct {
	Name     string   // test name for logging
	QEMUBin  string   // e.g. "qemu-system-x86_64"
	QEMUArgs []string // QEMU arguments (after "--")
	// setupFirmware is called to create any firmware files the test needs.
	// It returns updated qemuArgs with resolved firmware paths.
	SetupFirmware func(t *testing.T, tmpDir string, args []string) []string
	// BootMode is "UEFI" or "Legacy" for the Redfish PATCH request.
	BootMode string
	// ISOUrl is the URL of the boot ISO image to insert. Defaults to x86_64 netboot.xyz.
	ISOUrl string
}

// runBootFromCDROMTest is the shared test runner for all boot-from-CDROM variants.
func runBootFromCDROMTest(t *testing.T, cfg bootCDROMTestConfig) {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	if _, err := exec.LookPath(cfg.QEMUBin); err != nil {
		t.Skipf("skipping: %s not in PATH", cfg.QEMUBin)
	}

	// Build qemu-bmc binary
	projectRoot := findProjectRoot(t)
	bmcBin := filepath.Join(t.TempDir(), "qemu-bmc")
	t.Logf("Building qemu-bmc → %s", bmcBin)
	build := exec.Command("go", "build", "-o", bmcBin, "./cmd/qemu-bmc")
	build.Dir = projectRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}

	tmpDir := t.TempDir()
	qemuArgs := cfg.QEMUArgs
	if cfg.SetupFirmware != nil {
		qemuArgs = cfg.SetupFirmware(t, tmpDir, qemuArgs)
	}

	// Find free ports
	redfishPort := freePort(t)
	ipmiPort := freePort(t)

	// Create QMP socket path
	qmpSock := filepath.Join(tmpDir, "qmp.sock")

	// Prepend "--" separator for qemu-bmc
	fullArgs := append([]string{"--"}, qemuArgs...)

	// Set env for qemu-bmc
	env := append(os.Environ(),
		"QMP_SOCK="+qmpSock,
		"QEMU_BINARY="+cfg.QEMUBin,
		"POWER_ON_AT_START=true",
		"SERIAL_ADDR=",
		"IPMI_USER=admin",
		"IPMI_PASS=password",
		"REDFISH_ADDR=127.0.0.1",
		"REDFISH_PORT="+redfishPort,
		"IPMI_PORT="+ipmiPort,
		"VNC_ADDR=",
		"VM_IPMI_ADDR=",
	)

	// Start qemu-bmc
	cmd := exec.Command(bmcBin, fullArgs...)
	cmd.Env = env
	cmd.Dir = tmpDir

	// Capture stdout+stderr for scanning
	outputBuf := &syncBuffer{}
	cmd.Stdout = io.MultiWriter(outputBuf, &testWriter{t: t, prefix: "[stdout] "})
	cmd.Stderr = io.MultiWriter(outputBuf, &testWriter{t: t, prefix: "[stderr] "})

	t.Logf("Starting qemu-bmc (%s) on redfish port %s", cfg.Name, redfishPort)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting qemu-bmc: %v", err)
	}

	// Ensure cleanup
	done := make(chan struct{})
	defer func() {
		t.Log("Cleaning up: sending SIGTERM to qemu-bmc")
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Log("Cleanup: SIGTERM timed out, sending SIGKILL")
			_ = cmd.Process.Kill()
		}
	}()

	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	redfishBase := fmt.Sprintf("http://127.0.0.1:%s", redfishPort)

	// Step 1: Wait for Redfish to become ready
	t.Log("Waiting for Redfish to become ready...")
	if !waitForRedfish(t, redfishBase, 60*time.Second) {
		t.Fatalf("Redfish did not become ready within timeout")
	}
	t.Log("Redfish is ready")

	// Step 2: Insert boot ISO via VirtualMedia
	isoURL := cfg.ISOUrl
	if isoURL == "" {
		isoURL = "https://boot.netboot.xyz/ipxe/netboot.xyz.iso"
	}
	t.Logf("Inserting ISO: %s", isoURL)
	insertMedia(t, redfishBase, isoURL)

	// Step 3: Set one-time CD boot override
	t.Logf("Setting boot override to CD Once (mode=%s)...", cfg.BootMode)
	setBootOverride(t, redfishBase, cfg.BootMode)

	// Step 4: Force restart the VM
	t.Log("Sending ForceRestart...")
	forceRestart(t, redfishBase)

	// Step 5: Wait for "autoexec.ipxe" in output
	t.Log("Waiting for 'autoexec.ipxe' in VM console output...")
	deadline := time.After(5 * time.Minute)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			t.Fatalf("qemu-bmc exited before 'autoexec.ipxe' was seen.\nOutput:\n%s", outputBuf.String())
		case <-deadline:
			t.Fatalf("timed out waiting for 'autoexec.ipxe' in output.\nOutput tail:\n%s", outputBuf.Tail(4096))
		case <-ticker.C:
			if strings.Contains(outputBuf.String(), "autoexec.ipxe") {
				t.Log("SUCCESS: found 'autoexec.ipxe' in VM console output")
				return
			}
		}
	}
}

// --- Helpers ---

func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
}

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

func waitForRedfish(t *testing.T, base string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("GET", base+"/redfish/v1", nil)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		req.SetBasicAuth("admin", "password")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func insertMedia(t *testing.T, base, imageURL string) {
	t.Helper()
	body := fmt.Sprintf(`{"Image": %q, "Inserted": true}`, imageURL)
	req, err := http.NewRequest("POST",
		base+"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("creating insert request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "password")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("insert media request failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("insert media returned %d: %s", resp.StatusCode, respBody)
	}
	t.Logf("Insert media response: %s", respBody)
}

func setBootOverride(t *testing.T, base, bootMode string) {
	t.Helper()
	body := fmt.Sprintf(`{
		"Boot": {
			"BootSourceOverrideEnabled": "Once",
			"BootSourceOverrideTarget": "Cd",
			"BootSourceOverrideMode": %q
		}
	}`, bootMode)
	req, err := http.NewRequest("PATCH",
		base+"/redfish/v1/Systems/1",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("creating patch request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "password")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("set boot override request failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set boot override returned %d: %s", resp.StatusCode, respBody)
	}

	// Verify the response has correct boot override
	var sys map[string]interface{}
	json.Unmarshal(respBody, &sys)
	if boot, ok := sys["Boot"].(map[string]interface{}); ok {
		t.Logf("Boot override set: Enabled=%v Target=%v Mode=%v",
			boot["BootSourceOverrideEnabled"],
			boot["BootSourceOverrideTarget"],
			boot["BootSourceOverrideMode"])
	}
}

func forceRestart(t *testing.T, base string) {
	t.Helper()
	body := `{"ResetType": "ForceRestart"}`
	req, err := http.NewRequest("POST",
		base+"/redfish/v1/Systems/1/Actions/ComputerSystem.Reset",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("creating reset request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "password")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("force restart request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("force restart returned %d: %s", resp.StatusCode, respBody)
	}
	t.Log("ForceRestart sent successfully")
}

// findFirmware searches common paths for UEFI firmware files and returns
// the first matching (code, vars) pair. Returns empty strings if not found.
func findFirmware(paths []string) (string, string) {
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

// testWriter writes each line to t.Log with a prefix.
type testWriter struct {
	t      *testing.T
	prefix string
}

func (w *testWriter) Write(p []byte) (int, error) {
	scanner := bufio.NewScanner(bytes.NewReader(p))
	for scanner.Scan() {
		w.t.Log(w.prefix + scanner.Text())
	}
	return len(p), nil
}


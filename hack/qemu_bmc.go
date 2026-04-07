package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type config struct {
	VMArch       string
	VMBootMode   string
	OVMFCode     string
	OVMFVars     string
	QEMUDisk     string
	QEMUDiskSize string
	QEMUMemory   string
	QEMUCPUs     string
	QEMUIso      string
	RedfishAddr  string
	RedfishPort  string
	IPMIPort     string
	IPMIUser     string
	IPMIPass     string
	VNCPort      string
	SSHPort      string
}

type archProfile struct {
	QEMUBin   string
	Machine   string
	TCGCPU    string
	NetDevice string
}

func main() {
	if err := run(); err != nil {
		die(err.Error())
	}
}

func run() error {

	cfg := loadConfig()
	if err := parseFlags(cfg); err != nil {
		return err
	}

	hostArch, err := normalizeArch(runtime.GOARCH)
	if err != nil {
		return err
	}

	if cfg.VMArch == "" {
		cfg.VMArch = hostArch
	}
	cfg.VMArch, err = normalizeArch(cfg.VMArch)
	if err != nil {
		return err
	}

	profile, err := profileForArch(cfg.VMArch)
	if err != nil {
		return err
	}

	if err := checkDependencies(profile.QEMUBin); err != nil {
		return err
	}
	if err := ensureDisk(cfg.QEMUDisk, cfg.QEMUDiskSize); err != nil {
		return err
	}
	if cfg.QEMUIso != "" {
		if _, err := os.Stat(cfg.QEMUIso); err != nil {
			return fmt.Errorf("ISO not found: %s", cfg.QEMUIso)
		}
	}

	uefiArgs, err := buildUEFIArgs(cfg, profile.Machine)
	if err != nil {
		return err
	}
	accelArgs := buildAccelArgs(cfg.VMArch, hostArch, profile.TCGCPU)

	qmpSock, err := createQMPSockPath()
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(qmpSock)
	}()

	ipmiArgs, vmIPMIAddr, err := buildIPMIArgs(profile.QEMUBin)
	if err != nil {
		return err
	}
	cdromArgs, bootArgs, err := buildCDROMArgs(profile.Machine, cfg.QEMUIso)
	if err != nil {
		return err
	}

	qemuArgs := make([]string, 0, 64)
	qemuArgs = append(qemuArgs,
		accelArgs...,
	)
	qemuArgs = append(qemuArgs,
		"-machine", profile.Machine,
		"-m", cfg.QEMUMemory,
		"-smp", cfg.QEMUCPUs,
	)
	qemuArgs = append(qemuArgs, uefiArgs...)
	qemuArgs = append(qemuArgs,
		"-drive", fmt.Sprintf("file=%s,format=qcow2,if=virtio,cache=writeback", cfg.QEMUDisk),
	)
	qemuArgs = append(qemuArgs, cdromArgs...)
	qemuArgs = append(qemuArgs, bootArgs...)
	qemuArgs = append(qemuArgs,
		"-netdev", fmt.Sprintf("user,id=net0,hostfwd=tcp::%s-:22", cfg.SSHPort),
		"-device", fmt.Sprintf("%s,netdev=net0", profile.NetDevice),
	)
	qemuArgs = append(qemuArgs, ipmiArgs...)
	qemuArgs = append(qemuArgs, "-nographic")

	if err := os.Setenv("QMP_SOCK", qmpSock); err != nil {
		return err
	}
	_ = os.Setenv("QEMU_BINARY", profile.QEMUBin)
	_ = os.Setenv("POWER_ON_AT_START", "true")
	_ = os.Setenv("SERIAL_ADDR", "") // nographic handles serial via stdio
	_ = os.Setenv("IPMI_USER", cfg.IPMIUser)
	_ = os.Setenv("IPMI_PASS", cfg.IPMIPass)
	_ = os.Setenv("REDFISH_ADDR", cfg.RedfishAddr)
	_ = os.Setenv("REDFISH_PORT", cfg.RedfishPort)
	_ = os.Setenv("IPMI_PORT", cfg.IPMIPort)
	_ = os.Setenv("VNC_ADDR", "localhost:"+cfg.VNCPort)
	if vmIPMIAddr != "" {
		_ = os.Setenv("VM_IPMI_ADDR", vmIPMIAddr)
	}

	logf("Starting qemu-bmc (process mode)")
	logf("  QEMU     %s %s", profile.QEMUBin, strings.Join(qemuArgs, " "))
	logf("  Redfish  http://%s:%s/redfish/v1  (%s / %s)", cfg.RedfishAddr, cfg.RedfishPort, cfg.IPMIUser, cfg.IPMIPass)
	logf("  IPMI     127.0.0.1:%s", cfg.IPMIPort)
	logf("  VNC      localhost:%s", cfg.VNCPort)
	logf("  SSH      localhost:%s -> guest :22", cfg.SSHPort)
	logf("  Disk     %s", cfg.QEMUDisk)
	if cfg.QEMUIso != "" {
		logf("  CD-ROM   %s", cfg.QEMUIso)
	}

	bmcCmd, launchMsg, err := makeBMCCmd(cfg, qemuArgs)
	if err != nil {
		return err
	}
	bmcCmd.Stdout = os.Stdout
	bmcCmd.Stderr = os.Stderr
	bmcCmd.Stdin = os.Stdin
	bmcCmd.Dir, _ = os.Getwd()

	logf("Launching %s", launchMsg)

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		sig := <-sigCh
		if bmcCmd.Process != nil {
			_ = bmcCmd.Process.Signal(sig)
		}
	}()

	if err := bmcCmd.Run(); err != nil {
		return fmt.Errorf("qemu-bmc exited with error: %w", err)
	}
	return nil
}

func makeBMCCmd(cfg *config, qemuArgs []string) (*exec.Cmd, string, error) {
	bmcArgs := []string{"governance", "--reset-signal", "USR2"}
	if cfg.VMBootMode == "uefi" {
		liveVars := strings.TrimSuffix(cfg.QEMUDisk, filepath.Ext(cfg.QEMUDisk)) + "-uefi-vars.fd"
		if cfg.OVMFVars != "" {
			bmcArgs = append(bmcArgs, "--vars-file", liveVars, "--vars-template", cfg.OVMFVars)
		}
	}
	bmcArgs = append(bmcArgs, "--")
	bmcArgs = append(bmcArgs, qemuArgs...)

	if explicit := os.Getenv("QEMU_BMC_BIN"); explicit != "" {
		return exec.Command(explicit, bmcArgs...), explicit, nil
	}

	if fi, err := os.Stat("qemu-bmc"); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
		return exec.Command("./qemu-bmc", bmcArgs...), "./qemu-bmc", nil
	}

	if _, err := exec.LookPath("go"); err == nil {
		goArgs := append([]string{"run", "./cmd/qemu-bmc"}, bmcArgs...)
		return exec.Command("go", goArgs...), "go run ./cmd/qemu-bmc", nil
	}

	return nil, "", fmt.Errorf("qemu-bmc binary not found in current directory and 'go' is unavailable; build ./cmd/qemu-bmc or set QEMU_BMC_BIN")
}

func loadConfig() *config {
	wd, _ := os.Getwd()
	return &config{
		VMArch:       os.Getenv("VM_ARCH"),
		VMBootMode:   envOr("VM_BOOT_MODE", "uefi"),
		OVMFCode:     os.Getenv("OVMF_CODE"),
		OVMFVars:     os.Getenv("OVMF_VARS"),
		QEMUDisk:     envOr("QEMU_DISK", filepath.Join(wd, "vm-disk.qcow2")),
		QEMUDiskSize: envOr("QEMU_DISK_SIZE", "32G"),
		QEMUMemory:   envOr("QEMU_MEMORY", "2048"),
		QEMUCPUs:     envOr("QEMU_CPUS", "2"),
		QEMUIso:      os.Getenv("QEMU_ISO"),
		RedfishAddr:  envOr("REDFISH_ADDR", "127.0.0.1"),
		RedfishPort:  envOr("REDFISH_PORT", "8080"),
		IPMIPort:     envOr("IPMI_PORT", "6623"),
		IPMIUser:     envOr("IPMI_USER", "admin"),
		IPMIPass:     envOr("IPMI_PASS", "password"),
		VNCPort:      envOr("VNC_PORT", "5900"),
		SSHPort:      envOr("SSH_PORT", "2222"),
	}
}

func parseFlags(cfg *config) error {
	var useUEFI bool
	flag.StringVar(&cfg.VMArch, "arch", cfg.VMArch, "Guest architecture")
	flag.BoolVar(&useUEFI, "uefi", false, "Use UEFI firmware")
	flag.StringVar(&cfg.QEMUIso, "iso", cfg.QEMUIso, "Optional ISO path")
	flag.StringVar(&cfg.QEMUDisk, "disk", cfg.QEMUDisk, "Disk image path")
	flag.StringVar(&cfg.RedfishPort, "port", cfg.RedfishPort, "Redfish port")
	flag.Parse()
	if useUEFI {
		cfg.VMBootMode = "uefi"
	}
	if cfg.VMBootMode != "uefi" && cfg.VMBootMode != "bios" {
		return fmt.Errorf("VM_BOOT_MODE must be bios or uefi")
	}
	if mustAtoi(cfg.VNCPort) < 5900 {
		return fmt.Errorf("VNC_PORT must be >= 5900")
	}
	return nil
}

func envOr(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

func normalizeArch(v string) (string, error) {
	switch strings.ToLower(v) {
	case "x86_64", "amd64":
		return "x86_64", nil
	case "aarch64", "arm64":
		return "aarch64", nil
	case "arm", "armv7", "armv7l", "armv6", "armv6l":
		return "arm", nil
	case "riscv64":
		return "riscv64", nil
	case "ppc64", "ppc64le":
		return "ppc64", nil
	case "s390x":
		return "s390x", nil
	default:
		return "", fmt.Errorf("unknown architecture: %s", v)
	}
}

func profileForArch(vmArch string) (*archProfile, error) {
	switch vmArch {
	case "x86_64":
		return &archProfile{QEMUBin: "qemu-system-x86_64", Machine: "q35", TCGCPU: "qemu64", NetDevice: "virtio-net-pci"}, nil
	case "aarch64":
		return &archProfile{QEMUBin: "qemu-system-aarch64", Machine: "virt", TCGCPU: "cortex-a57", NetDevice: "virtio-net-pci"}, nil
	case "arm":
		return &archProfile{QEMUBin: "qemu-system-arm", Machine: "virt", TCGCPU: "cortex-a15", NetDevice: "virtio-net-pci"}, nil
	case "riscv64":
		return &archProfile{QEMUBin: "qemu-system-riscv64", Machine: "virt", TCGCPU: "rv64", NetDevice: "virtio-net-pci"}, nil
	case "ppc64":
		return &archProfile{QEMUBin: "qemu-system-ppc64", Machine: "pseries", TCGCPU: "POWER9", NetDevice: "virtio-net-pci"}, nil
	case "s390x":
		return &archProfile{QEMUBin: "qemu-system-s390x", Machine: "s390-ccw-virtio", TCGCPU: "qemu", NetDevice: "virtio-net-ccw"}, nil
	default:
		return nil, fmt.Errorf("unsupported architecture: %s", vmArch)
	}
}

func buildUEFIArgs(cfg *config, machine string) ([]string, error) {
	if cfg.VMBootMode != "uefi" {
		return nil, nil
	}
	if cfg.VMArch == "ppc64" || cfg.VMArch == "s390x" {
		return nil, fmt.Errorf("UEFI mode is not supported for %s", cfg.VMArch)
	}

	code := cfg.OVMFCode
	vars := cfg.OVMFVars
	if code == "" || vars == "" {
		foundCode, foundVars, err := detectFirmwarePair(cfg.VMArch, machine)
		if err != nil {
			return nil, err
		}
		code = foundCode
		vars = foundVars
	}
	cfg.OVMFCode = code
	cfg.OVMFVars = vars

	liveVars := strings.TrimSuffix(cfg.QEMUDisk, filepath.Ext(cfg.QEMUDisk)) + "-uefi-vars.fd"
	if _, err := os.Stat(liveVars); errors.Is(err, os.ErrNotExist) {
		logf("Creating UEFI NVRAM: %s", liveVars)
		in, err := os.ReadFile(vars)
		if err != nil {
			return nil, fmt.Errorf("read OVMF vars template: %w", err)
		}
		if err := os.WriteFile(liveVars, in, 0o644); err != nil {
			return nil, fmt.Errorf("write live vars file: %w", err)
		}
	}

	return []string{
		"-drive", fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", code),
		"-drive", fmt.Sprintf("if=pflash,format=raw,file=%s", liveVars),
	}, nil
}

func detectFirmwarePair(vmArch, machine string) (string, string, error) {
	pairs := firmwarePairsForArch(vmArch)
	limit := int64(0)
	if machine == "q35" {
		limit = 8 * 1024 * 1024
	}

	for _, pair := range pairs {
		parts := strings.SplitN(pair, ":", 2)
		if len(parts) != 2 {
			continue
		}
		code := parts[0]
		vars := parts[1]
		if !fileExists(code) || !fileExists(vars) {
			continue
		}
		if limit > 0 {
			szCode, _ := fileSize(code)
			szVars, _ := fileSize(vars)
			if szCode+szVars >= limit {
				logf("Skipping %s (combined %d bytes >= %d byte q35 limit)", code, szCode+szVars, limit)
				continue
			}
		}
		return code, vars, nil
	}
	return "", "", fmt.Errorf("UEFI firmware not found for %s (set OVMF_CODE and OVMF_VARS)", vmArch)
}

func firmwarePairsForArch(vmArch string) []string {
	switch vmArch {
	case "x86_64":
		return []string{
			"/opt/homebrew/share/qemu/edk2-x86_64-code.fd:/opt/homebrew/share/qemu/edk2-i386-vars.fd",
			"/usr/local/share/qemu/edk2-x86_64-code.fd:/usr/local/share/qemu/edk2-i386-vars.fd",
			"/usr/share/OVMF/OVMF_CODE.fd:/usr/share/OVMF/OVMF_VARS.fd",
			"/usr/share/OVMF/OVMF_CODE_4M.fd:/usr/share/OVMF/OVMF_VARS_4M.fd",
			"/usr/share/edk2/ovmf/OVMF_CODE.fd:/usr/share/edk2/ovmf/OVMF_VARS.fd",
			"/usr/share/ovmf/x64/OVMF_CODE.fd:/usr/share/ovmf/x64/OVMF_VARS.fd",
		}
	case "aarch64":
		return []string{
			"/opt/homebrew/share/qemu/edk2-aarch64-code.fd:/opt/homebrew/share/qemu/edk2-arm-vars.fd",
			"/usr/local/share/qemu/edk2-aarch64-code.fd:/usr/local/share/qemu/edk2-arm-vars.fd",
			"/usr/share/AAVMF/AAVMF_CODE.fd:/usr/share/AAVMF/AAVMF_VARS.fd",
			"/usr/share/qemu-efi-aarch64/AAVMF_CODE.fd:/usr/share/qemu-efi-aarch64/AAVMF_VARS.fd",
			"/usr/share/edk2/aarch64/QEMU_EFI-pflash.raw:/usr/share/edk2/aarch64/vars-template-pflash.raw",
		}
	case "arm":
		return []string{
			"/opt/homebrew/share/qemu/edk2-arm-code.fd:/opt/homebrew/share/qemu/edk2-arm-vars.fd",
			"/usr/local/share/qemu/edk2-arm-code.fd:/usr/local/share/qemu/edk2-arm-vars.fd",
			"/usr/share/AAVMF/AAVMF32_CODE.fd:/usr/share/AAVMF/AAVMF32_VARS.fd",
		}
	case "riscv64":
		return []string{
			"/opt/homebrew/share/qemu/edk2-riscv-code.fd:/opt/homebrew/share/qemu/edk2-riscv-vars.fd",
			"/usr/local/share/qemu/edk2-riscv-code.fd:/usr/local/share/qemu/edk2-riscv-vars.fd",
			"/usr/share/edk2/riscv/RISCV_VIRT_CODE.fd:/usr/share/edk2/riscv/RISCV_VIRT_VARS.fd",
		}
	default:
		return nil
	}
}

func checkDependencies(qemuBin string) error {
	for _, dep := range []string{"qemu-bmc", "qemu-img", qemuBin} {
		if _, err := exec.LookPath(dep); err != nil {
			return fmt.Errorf("%s not found in PATH", dep)
		}
	}
	return nil
}

func ensureDisk(path, size string) error {
	if fileExists(path) {
		return nil
	}
	logf("Creating disk image: %s (%s)", path, size)
	cmd := exec.Command("qemu-img", "create", "-f", "qcow2", path, size)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildAccelArgs(vmArch, hostArch, tcgCPU string) []string {
	if vmArch != hostArch {
		logf("Note: VM arch (%s) differs from host (%s) - using TCG", vmArch, hostArch)
		return []string{"-accel", "tcg", "-cpu", tcgCPU}
	}

	switch runtime.GOOS {
	case "linux":
		if fileExists("/dev/kvm") {
			return []string{"-accel", "kvm", "-cpu", "host"}
		}
		logf("Warning: /dev/kvm not available, falling back to TCG")
		return []string{"-accel", "tcg", "-cpu", tcgCPU}
	case "darwin":
		return []string{"-accel", "hvf", "-cpu", "host"}
	default:
		return []string{"-accel", "tcg", "-cpu", tcgCPU}
	}
}

func createQMPSockPath() (string, error) {
	tmpDir := envOr("TMPDIR", "/tmp")
	tmpDir = strings.TrimRight(tmpDir, "/")
	f, err := os.CreateTemp(tmpDir, "qemu-bmc-qmp-")
	if err != nil {
		return "", fmt.Errorf("create temp qmp socket path: %w", err)
	}
	path := f.Name()
	_ = f.Close()
	_ = os.Remove(path)
	return path, nil
}

func buildIPMIArgs(qemuBin string) ([]string, string, error) {
	help, err := exec.Command(qemuBin, "-device", "help").CombinedOutput()
	if err != nil {
		return nil, "", fmt.Errorf("probe QEMU devices: %w", err)
	}
	text := string(help)
	if !strings.Contains(text, `"ipmi-bmc-extern"`) {
		logf("Note: %s has no ipmi-bmc-extern - in-band IPMI unavailable", qemuBin)
		return nil, "", nil
	}
	kcsDevice := "isa-ipmi-kcs"
	if strings.Contains(text, `"pci-ipmi-kcs"`) {
		kcsDevice = "pci-ipmi-kcs"
	}
	args := []string{
		"-chardev", "socket,id=ipmi0,host=localhost,port=9002,reconnect-ms=10000",
		"-device", "ipmi-bmc-extern,id=bmc0,chardev=ipmi0",
		"-device", fmt.Sprintf("%s,bmc=bmc0", kcsDevice),
	}
	return args, ":9002", nil
}

func buildCDROMArgs(machine, iso string) ([]string, []string, error) {
	cdromDrive := "if=none,id=ide0-cd0,media=cdrom"
	if iso != "" {
		cdromDrive = fmt.Sprintf("if=none,id=ide0-cd0,media=cdrom,file=%s,readonly=on", iso)
	}
	bootIndex := ""
	if iso != "" {
		bootIndex = ",bootindex=1"
	}

	var cdromArgs []string
	switch machine {
	case "q35":
		cdromArgs = []string{
			"-device", "ich9-ahci,id=ahci0",
			"-device", "ide-cd,drive=ide0-cd0,bus=ahci0.0" + bootIndex,
			"-drive", cdromDrive,
		}
	case "virt", "pseries":
		cdromArgs = []string{
			"-device", "virtio-scsi-pci,id=scsi0",
			"-device", "scsi-cd,drive=ide0-cd0,bus=scsi0.0" + bootIndex,
			"-drive", cdromDrive,
		}
	case "s390-ccw-virtio":
		cdromArgs = []string{
			"-device", "virtio-scsi-ccw,id=scsi0",
			"-device", "scsi-cd,drive=ide0-cd0,bus=scsi0.0" + bootIndex,
			"-drive", cdromDrive,
		}
	default:
		return nil, nil, fmt.Errorf("no CD-ROM configuration for machine type %q", machine)
	}

	bootArgs := []string{}
	return cdromArgs, bootArgs, nil
}

func waitForQMPReady(sockPath string, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return fmt.Errorf("QEMU exited before QMP became ready")
		}
		if qmpReady(sockPath) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("QMP did not become ready within %s", timeout)
}

func qmpReady(sockPath string) bool {
	if _, err := os.Stat(sockPath); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		return false
	}
	defer func() {
		_ = conn.Close()
	}()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	return strings.Contains(line, `"QMP"`)
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func mustAtoi(v string) int {
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			die("expected numeric value: " + v)
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[qemu-bmc] "+format+"\n", args...)
}

func die(msg string) {
	fmt.Fprintf(os.Stderr, "[qemu-bmc] %s\n", msg)
	os.Exit(1)
}

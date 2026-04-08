package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestBootFromCDROM_UEFI_aarch64 boots a UEFI aarch64 VM from a USB disk
// image containing netboot.xyz and verifies "autoexec.ipxe" appears in output.
// The aarch64 "virt" machine uses USB storage (qemu-xhci) since there is no
// aarch64 ISO available — the ARM64 netboot.xyz ships as a raw disk image.
// The drive ID is kept as "ide0-cd0" so the existing BlockdevChangeMedium
// path (VirtualMedia) works without changes.
func TestBootFromCDROM_UEFI_aarch64(t *testing.T) {
	firmwarePaths := []string{
		"/opt/homebrew/share/qemu/edk2-aarch64-code.fd:/opt/homebrew/share/qemu/edk2-arm-vars.fd",
		"/usr/local/share/qemu/edk2-aarch64-code.fd:/usr/local/share/qemu/edk2-arm-vars.fd",
		"/usr/share/AAVMF/AAVMF_CODE.fd:/usr/share/AAVMF/AAVMF_VARS.fd",
		"/usr/share/qemu-efi-aarch64/AAVMF_CODE.fd:/usr/share/qemu-efi-aarch64/AAVMF_VARS.fd",
		"/usr/share/edk2/aarch64/QEMU_EFI-pflash.raw:/usr/share/edk2/aarch64/vars-template-pflash.raw",
	}

	ovmfCode, ovmfVarsTemplate := findFirmware(firmwarePaths)
	if ovmfCode == "" {
		t.Skip("skipping: aarch64 UEFI firmware not found")
	}

	runBootFromCDROMTest(t, bootCDROMTestConfig{
		Name:     "UEFI-aarch64",
		QEMUBin:  "qemu-system-aarch64",
		BootMode: "UEFI",
		ISOUrl:   "https://boot.netboot.xyz/ipxe/netboot.xyz-arm64.img",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "cortex-a57",
			"-machine", "virt",
			"-m", "512",
			"-smp", "1",
			"-drive", fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", ovmfCode),
			"-drive", "UEFI_VARS_PLACEHOLDER",
			// USB storage with removable=true supports blockdev-change-medium.
			// Drive ID "ide0-cd0" matches what machine.InsertMedia() uses.
			// Media is cached locally by the proxy, so QEMU gets a file path.
			"-drive", "if=none,id=ide0-cd0",
			"-device", "qemu-xhci,id=xhci0",
			"-device", "usb-storage,bus=xhci0.0,drive=ide0-cd0,removable=true",
			"-nographic",
		},
		SetupFirmware: func(t *testing.T, tmpDir string, args []string) []string {
			t.Helper()
			liveVars := filepath.Join(tmpDir, "uefi-vars.fd")
			data, err := os.ReadFile(ovmfVarsTemplate)
			if err != nil {
				t.Fatalf("reading AAVMF vars template: %v", err)
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
		},
	})
}


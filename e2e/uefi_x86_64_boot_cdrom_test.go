package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestBootFromCDROM_UEFI_x86_64 boots a UEFI x86_64 VM from a CD-ROM
// containing netboot.xyz and verifies "autoexec.ipxe" appears in output.
func TestBootFromCDROM_UEFI_x86_64(t *testing.T) {
	firmwarePaths := []string{
		"/opt/homebrew/share/qemu/edk2-x86_64-code.fd:/opt/homebrew/share/qemu/edk2-i386-vars.fd",
		"/usr/local/share/qemu/edk2-x86_64-code.fd:/usr/local/share/qemu/edk2-i386-vars.fd",
		"/usr/share/OVMF/OVMF_CODE.fd:/usr/share/OVMF/OVMF_VARS.fd",
		"/usr/share/OVMF/OVMF_CODE_4M.fd:/usr/share/OVMF/OVMF_VARS_4M.fd",
		"/usr/share/edk2/ovmf/OVMF_CODE.fd:/usr/share/edk2/ovmf/OVMF_VARS.fd",
		"/usr/share/ovmf/x64/OVMF_CODE.fd:/usr/share/ovmf/x64/OVMF_VARS.fd",
	}

	ovmfCode, ovmfVarsTemplate := findFirmware(firmwarePaths)
	if ovmfCode == "" {
		t.Skip("skipping: x86_64 UEFI firmware not found")
	}

	runBootFromCDROMTest(t, bootCDROMTestConfig{
		Name:     "UEFI-x86_64",
		QEMUBin:  "qemu-system-x86_64",
		BootMode: "UEFI",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "512",
			"-smp", "1",
			"-drive", fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", ovmfCode),
			"-drive", "UEFI_VARS_PLACEHOLDER",
			"-drive", "if=none,id=ide0-cd0,media=cdrom",
			"-device", "ich9-ahci,id=ahci0",
			"-device", "ide-cd,drive=ide0-cd0,bus=ahci0.0",
			"-nographic",
		},
		SetupFirmware: func(t *testing.T, tmpDir string, args []string) []string {
			t.Helper()
			liveVars := filepath.Join(tmpDir, "uefi-vars.fd")
			data, err := os.ReadFile(ovmfVarsTemplate)
			if err != nil {
				t.Fatalf("reading OVMF vars template: %v", err)
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

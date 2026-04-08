package bmctest_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/steigr/qemu-bmc/pkg/bmctest"
)

// TestExample_BootFromCDROM_UEFI_x86_64 demonstrates the full workflow
// of booting a UEFI x86_64 VM from a CD-ROM image inserted via the
// Redfish VirtualMedia API.
//
// This is the canonical example for projects that need to PXE/CD boot
// virtual machines controlled by qemu-bmc.
func TestExample_BootFromCDROM_UEFI_x86_64(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	// Find UEFI firmware on the system.
	ovmfCode, ovmfVarsTemplate := bmctest.FindFirmware(bmctest.X86_64UEFIPaths)
	if ovmfCode == "" {
		t.Skip("skipping: x86_64 UEFI firmware not found")
	}

	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin:  "qemu-system-x86_64",
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
		SetupFirmware: bmctest.SetupUEFIVars(ovmfVarsTemplate),
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	// Step 1: Insert the netboot.xyz ISO via VirtualMedia.
	isoURL := "https://boot.netboot.xyz/ipxe/netboot.xyz.iso"
	t.Logf("Inserting ISO: %s", isoURL)
	bmc.InsertMedia(isoURL)

	// Step 2: Set one-time CD boot override.
	t.Log("Setting boot override to Once/Cd/UEFI...")
	bmc.SetBootOverride("Once", "Cd", "UEFI")

	// Step 3: Force restart to apply boot override.
	t.Log("Sending ForceRestart...")
	bmc.ResetSystem("ForceRestart")

	// Step 4: Wait for netboot.xyz to appear in console output.
	t.Log("Waiting for 'autoexec.ipxe' in VM console output...")
	if bmc.WaitForOutput("autoexec.ipxe", 5*time.Minute) {
		t.Log("SUCCESS: found 'autoexec.ipxe' — VM booted from CD-ROM")
	} else {
		t.Fatalf("timed out waiting for 'autoexec.ipxe'.\nOutput tail:\n%s", bmc.OutputTail(4096))
	}
}

// TestExample_BootFromCDROM_BIOS_x86_64 demonstrates booting a BIOS
// (SeaBIOS) x86_64 VM from a CD-ROM image.
func TestExample_BootFromCDROM_BIOS_x86_64(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin:  "qemu-system-x86_64",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "512",
			"-smp", "1",
			"-drive", "if=none,id=ide0-cd0,media=cdrom",
			"-device", "ich9-ahci,id=ahci0",
			"-device", "ide-cd,drive=ide0-cd0,bus=ahci0.0",
			"-nographic",
		},
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	bmc.InsertMedia("https://boot.netboot.xyz/ipxe/netboot.xyz.iso")
	bmc.SetBootOverride("Once", "Cd", "Legacy")
	bmc.ResetSystem("ForceRestart")

	if bmc.WaitForOutput("autoexec.ipxe", 5*time.Minute) {
		t.Log("SUCCESS: BIOS VM booted from CD-ROM")
	} else {
		t.Fatalf("timed out waiting for 'autoexec.ipxe'.\nOutput tail:\n%s", bmc.OutputTail(4096))
	}
}

// TestExample_BootFromCDROM_UEFI_aarch64 demonstrates booting a UEFI
// aarch64 VM from a USB disk image (since aarch64 uses USB storage
// rather than IDE CD-ROM).
func TestExample_BootFromCDROM_UEFI_aarch64(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	ovmfCode, ovmfVarsTemplate := bmctest.FindFirmware(bmctest.AArch64UEFIPaths)
	if ovmfCode == "" {
		t.Skip("skipping: aarch64 UEFI firmware not found")
	}

	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin:  "qemu-system-aarch64",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "cortex-a57",
			"-machine", "virt",
			"-m", "512",
			"-smp", "1",
			"-drive", fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", ovmfCode),
			"-drive", "UEFI_VARS_PLACEHOLDER",
			"-drive", "if=none,id=ide0-cd0",
			"-device", "qemu-xhci,id=xhci0",
			"-device", "usb-storage,bus=xhci0.0,drive=ide0-cd0,removable=true",
			"-nographic",
		},
		SetupFirmware: bmctest.SetupUEFIVars(ovmfVarsTemplate),
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	// aarch64 netboot.xyz ships as a raw disk image, not an ISO.
	bmc.InsertMedia("https://boot.netboot.xyz/ipxe/netboot.xyz-arm64.img")
	bmc.SetBootOverride("Once", "Cd", "UEFI")
	bmc.ResetSystem("ForceRestart")

	if bmc.WaitForOutput("autoexec.ipxe", 5*time.Minute) {
		t.Log("SUCCESS: aarch64 VM booted from USB media")
	} else {
		t.Fatalf("timed out waiting for 'autoexec.ipxe'.\nOutput tail:\n%s", bmc.OutputTail(4096))
	}
}


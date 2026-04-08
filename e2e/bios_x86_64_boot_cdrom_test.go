package e2e

import (
	"testing"
)

// TestBootFromCDROM_BIOS_x86_64 boots a BIOS (SeaBIOS) x86_64 VM from a
// CD-ROM containing netboot.xyz and verifies "autoexec.ipxe" appears in
// output. SeaBIOS uses the -boot flag for boot device selection.
func TestBootFromCDROM_BIOS_x86_64(t *testing.T) {
	runBootFromCDROMTest(t, bootCDROMTestConfig{
		Name:     "BIOS-x86_64",
		QEMUBin:  "qemu-system-x86_64",
		BootMode: "Legacy",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "512",
			"-smp", "1",
			// No pflash firmware — SeaBIOS is the default
			"-drive", "if=none,id=ide0-cd0,media=cdrom",
			"-device", "ich9-ahci,id=ahci0",
			"-device", "ide-cd,drive=ide0-cd0,bus=ahci0.0",
			"-nographic",
		},
	})
}


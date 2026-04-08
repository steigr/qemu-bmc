# bmctest — Reusable e2e test helpers for qemu-bmc

Package `bmctest` provides Go test helpers for end-to-end testing of virtual machines managed by [qemu-bmc](https://github.com/steigr/qemu-bmc). It wires up the qemu-bmc internal components (QMP client, process manager, machine state, Redfish server, IPMI server) directly in the test process — no binary compilation or subprocess needed.

## Installation

```bash
go get github.com/steigr/qemu-bmc/pkg/bmctest
```

## Prerequisites

- **QEMU**: `qemu-system-x86_64` or `qemu-system-aarch64` installed and in `$PATH`
- **UEFI firmware** (optional): OVMF/AAVMF for UEFI boot tests

## Architecture

Unlike traditional e2e testing that builds and runs qemu-bmc as a subprocess, `bmctest` runs everything **in-process**:

```
Test Process
├── QEMU process manager  (starts/stops qemu-system-*)
├── QMP client             (communicates with QEMU via UNIX socket)
├── Machine layer          (wraps QMP + process lifecycle)
├── Redfish HTTP server    (on random port, HTTP only)
├── IPMI UDP server        (on random port)
└── Output capture         (QEMU stdout/stderr)
```

Benefits:
- **No compilation step** — tests start instantly
- **Direct access** — `bmc.Machine` exposes the machine interface for advanced scenarios
- **Clean isolation** — each test gets its own servers on random ports
- **Automatic cleanup** — `defer bmc.Cleanup()` stops everything

## Quick Start

```go
package myproject_test

import (
    "testing"
    "time"

    "github.com/steigr/qemu-bmc/pkg/bmctest"
)

func TestMyVM(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping e2e test in short mode")
    }

    // Start a BIOS x86_64 VM managed by qemu-bmc.
    bmc := bmctest.New(t, bmctest.Config{
        QEMUBin: "qemu-system-x86_64",
        QEMUArgs: []string{
            "-accel", "tcg",
            "-cpu", "qemu64",
            "-machine", "q35",
            "-m", "512",
            "-smp", "1",
            "-nographic",
        },
    })
    defer bmc.Cleanup()

    // Wait for Redfish API to become available.
    bmc.WaitReady(60 * time.Second)

    // Check power state.
    if state := bmc.GetPowerState(); state != "On" {
        t.Fatalf("expected On, got %s", state)
    }

    // Insert ISO and boot from CD.
    bmc.InsertMedia("https://boot.netboot.xyz/ipxe/netboot.xyz.iso")
    bmc.SetBootOverride("Once", "Cd", "Legacy")
    bmc.ResetSystem("ForceRestart")

    // Wait for the boot media to load.
    if !bmc.WaitForOutput("autoexec.ipxe", 5*time.Minute) {
        t.Fatal("VM did not boot from CD")
    }
}
```

## API Overview

### `bmctest.New(t, cfg)` → `*BMC`

Creates and starts a qemu-bmc instance. The `Config` struct controls:

| Field | Description | Default |
|---|---|---|
| `QEMUBin` | QEMU binary name (required) | — |
| `QEMUArgs` | QEMU command-line arguments | — |
| `IPMIUser` / `IPMIPass` | Credentials | `admin` / `password` |
| `PowerOnAtStart` | Start VM automatically | `true` |
| `SetupFirmware` | Callback to prepare UEFI vars | `nil` |

### BMC Instance Methods

**Lifecycle:**
- `bmc.Cleanup()` — Stop QEMU process and shut down servers
- `bmc.WaitReady(timeout)` — Block until Redfish responds
- `bmc.Machine` — Direct access to the machine interface for advanced scenarios

**Power Management:**
- `bmc.GetPowerState()` → `"On"` or `"Off"`
- `bmc.ResetSystem(resetType)` — Send reset action (`On`, `ForceOff`, `ForceRestart`, `GracefulShutdown`, `GracefulRestart`)

**Boot Override:**
- `bmc.GetBootOverride()` → `(enabled, target, mode)`
- `bmc.SetBootOverride(enabled, target, mode)` — Set boot source override

**Virtual Media:**
- `bmc.InsertMedia(imageURL)` — Insert ISO/IMG via Redfish
- `bmc.EjectMedia()` — Eject current media
- `bmc.GetVirtualMedia()` — Get VirtualMedia resource

**Redfish Resources:**
- `bmc.GetServiceRoot()` — Service root document
- `bmc.GetSystem()` — ComputerSystem resource
- `bmc.GetManager()` — Manager resource
- `bmc.GetChassis()` — Chassis resource

**Output Inspection:**
- `bmc.Output()` — All captured stdout+stderr
- `bmc.OutputTail(n)` — Last n bytes of output
- `bmc.WaitForOutput(substr, timeout)` — Wait for string in output

**Raw Requests:**
- `bmc.RedfishRequestRaw(method, path, body)` → `*http.Response`

### Utility Functions

- `bmctest.FindFirmware(paths)` — Search for UEFI firmware files
- `bmctest.SetupUEFIVars(template)` — Common SetupFirmware callback
- `bmctest.BoolPtr(v)` — Helper for `Config.PowerOnAtStart`

### Firmware Path Constants

- `bmctest.X86_64UEFIPaths` — Common x86_64 OVMF firmware locations
- `bmctest.AArch64UEFIPaths` — Common aarch64 AAVMF firmware locations

## Examples

See the test files in this package for complete examples:

- [`examples_test.go`](examples_test.go) — Power management, boot override, virtual media, Redfish API exploration
- [`boot_cdrom_test.go`](boot_cdrom_test.go) — Full boot-from-CDROM workflows (BIOS x86_64, UEFI x86_64, UEFI aarch64)

## UEFI Boot Example

```go
func TestUEFIBoot(t *testing.T) {
    ovmfCode, ovmfVars := bmctest.FindFirmware(bmctest.X86_64UEFIPaths)
    if ovmfCode == "" {
        t.Skip("UEFI firmware not found")
    }

    bmc := bmctest.New(t, bmctest.Config{
        QEMUBin: "qemu-system-x86_64",
        QEMUArgs: []string{
            "-accel", "tcg", "-cpu", "qemu64", "-machine", "q35",
            "-m", "512", "-smp", "1",
            "-drive", fmt.Sprintf("if=pflash,format=raw,readonly=on,file=%s", ovmfCode),
            "-drive", "UEFI_VARS_PLACEHOLDER",
            "-drive", "if=none,id=ide0-cd0,media=cdrom",
            "-device", "ich9-ahci,id=ahci0",
            "-device", "ide-cd,drive=ide0-cd0,bus=ahci0.0",
            "-nographic",
        },
        SetupFirmware: bmctest.SetupUEFIVars(ovmfVars),
    })
    defer bmc.Cleanup()
    bmc.WaitReady(60 * time.Second)

    bmc.InsertMedia("https://boot.netboot.xyz/ipxe/netboot.xyz.iso")
    bmc.SetBootOverride("Once", "Cd", "UEFI")
    bmc.ResetSystem("ForceRestart")

    if !bmc.WaitForOutput("autoexec.ipxe", 5*time.Minute) {
        t.Fatal("UEFI CD boot failed")
    }
}
```

## Direct Machine Access

For advanced scenarios, you can access the machine interface directly:

```go
bmc := bmctest.New(t, bmctest.Config{
    QEMUBin:  "qemu-system-x86_64",
    QEMUArgs: []string{"-accel", "tcg", "-m", "512", "-nographic"},
})
defer bmc.Cleanup()
bmc.WaitReady(60 * time.Second)

// Use the machine interface directly (bypasses Redfish)
state, err := bmc.Machine.GetPowerState()
boot := bmc.Machine.GetBootOverride()
```


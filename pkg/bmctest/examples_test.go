// Package bmctest_test contains example tests demonstrating how to use
// the bmctest package for e2e testing of qemu-bmc.
//
// These examples are meant to be copied and adapted by other projects.
// Each test shows a different use case: power management, boot override,
// virtual media, and Redfish API exploration.
//
// Prerequisites:
//   - qemu-system-x86_64 or qemu-system-aarch64 installed
//   - For UEFI tests: OVMF/AAVMF firmware files
//   - Go toolchain (to build qemu-bmc from source)
//
// Run examples:
//
//	go test -v -run TestExample ./pkg/bmctest/ -timeout 10m
package bmctest_test

import (
	"testing"
	"time"

	"github.com/steigr/qemu-bmc/pkg/bmctest"
)

// TestExample_PowerManagement demonstrates power on/off via Redfish.
func TestExample_PowerManagement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	// Start a minimal BIOS x86_64 VM via qemu-bmc.
	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin: "qemu-system-x86_64",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "256",
			"-smp", "1",
			"-nographic",
		},
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	// VM should be powered on (PowerOnAtStart defaults to true).
	state := bmc.GetPowerState()
	if state != "On" {
		t.Fatalf("expected PowerState=On, got %q", state)
	}
	t.Logf("Power state: %s", state)

	// Force power off.
	bmc.ResetSystem("ForceOff")
	time.Sleep(2 * time.Second)

	state = bmc.GetPowerState()
	if state != "Off" {
		t.Fatalf("expected PowerState=Off after ForceOff, got %q", state)
	}
	t.Logf("Power state after ForceOff: %s", state)

	// Power back on.
	bmc.ResetSystem("On")
	time.Sleep(3 * time.Second)

	state = bmc.GetPowerState()
	if state != "On" {
		t.Fatalf("expected PowerState=On after power on, got %q", state)
	}
	t.Log("Power cycle complete")
}

// TestExample_RedfishServiceRoot demonstrates reading the Redfish service root.
func TestExample_RedfishServiceRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin: "qemu-system-x86_64",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "256",
			"-smp", "1",
			"-nographic",
		},
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	root := bmc.GetServiceRoot()

	// Verify essential fields
	if root["RedfishVersion"] == nil {
		t.Fatal("RedfishVersion missing from service root")
	}
	t.Logf("Redfish version: %v", root["RedfishVersion"])

	if root["@odata.type"] == nil {
		t.Fatal("@odata.type missing from service root")
	}
	t.Logf("Service root type: %v", root["@odata.type"])

	// Check that Systems, Managers, Chassis links are present
	for _, key := range []string{"Systems", "Managers", "Chassis"} {
		link, ok := root[key].(map[string]interface{})
		if !ok {
			t.Fatalf("%s link missing from service root", key)
		}
		t.Logf("%s: %v", key, link["@odata.id"])
	}
}

// TestExample_BootOverride demonstrates setting boot source override.
func TestExample_BootOverride(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin: "qemu-system-x86_64",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "256",
			"-smp", "1",
			"-nographic",
		},
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	// Initially boot override should be disabled.
	enabled, target, mode := bmc.GetBootOverride()
	t.Logf("Initial boot override: enabled=%s target=%s mode=%s", enabled, target, mode)

	if enabled != "Disabled" {
		t.Fatalf("expected initial BootSourceOverrideEnabled=Disabled, got %q", enabled)
	}

	// Set one-time PXE boot.
	bmc.SetBootOverride("Once", "Pxe", "UEFI")

	enabled, target, mode = bmc.GetBootOverride()
	if enabled != "Once" || target != "Pxe" || mode != "UEFI" {
		t.Fatalf("boot override not set correctly: enabled=%s target=%s mode=%s", enabled, target, mode)
	}
	t.Log("Boot override set to Once/Pxe/UEFI")

	// Set continuous HDD boot.
	bmc.SetBootOverride("Continuous", "Hdd", "Legacy")

	enabled, target, mode = bmc.GetBootOverride()
	if enabled != "Continuous" || target != "Hdd" || mode != "Legacy" {
		t.Fatalf("boot override not set correctly: enabled=%s target=%s mode=%s", enabled, target, mode)
	}
	t.Log("Boot override set to Continuous/Hdd/Legacy")

	// Disable boot override.
	bmc.SetBootOverride("Disabled", "None", "UEFI")
	enabled, _, _ = bmc.GetBootOverride()
	if enabled != "Disabled" {
		t.Fatalf("expected BootSourceOverrideEnabled=Disabled, got %q", enabled)
	}
	t.Log("Boot override disabled")
}

// TestExample_VirtualMedia demonstrates inserting and ejecting virtual media.
func TestExample_VirtualMedia(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin: "qemu-system-x86_64",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "256",
			"-smp", "1",
			"-drive", "if=none,id=ide0-cd0,media=cdrom",
			"-device", "ich9-ahci,id=ahci0",
			"-device", "ide-cd,drive=ide0-cd0,bus=ahci0.0",
			"-nographic",
		},
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	// Check initial state: no media inserted.
	vm := bmc.GetVirtualMedia()
	if vm["Inserted"] != false {
		t.Fatalf("expected no media inserted initially, got Inserted=%v", vm["Inserted"])
	}
	t.Log("No media inserted initially")

	// Insert a boot ISO.
	bmc.InsertMedia("https://boot.netboot.xyz/ipxe/netboot.xyz.iso")

	// Verify media is now inserted.
	vm = bmc.GetVirtualMedia()
	if vm["Inserted"] != true {
		t.Fatalf("expected media inserted after InsertMedia, got Inserted=%v", vm["Inserted"])
	}
	t.Logf("Media inserted: Image=%v", vm["Image"])

	// Eject media.
	bmc.EjectMedia()

	vm = bmc.GetVirtualMedia()
	if vm["Inserted"] != false {
		t.Fatalf("expected no media after EjectMedia, got Inserted=%v", vm["Inserted"])
	}
	t.Log("Media ejected successfully")
}

// TestExample_PowerOnAtStartDisabled demonstrates starting with VM powered off.
func TestExample_PowerOnAtStartDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin: "qemu-system-x86_64",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "256",
			"-smp", "1",
			"-nographic",
		},
		PowerOnAtStart: bmctest.BoolPtr(false),
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	// VM should be powered off.
	state := bmc.GetPowerState()
	if state != "Off" {
		t.Fatalf("expected PowerState=Off with PowerOnAtStart=false, got %q", state)
	}
	t.Log("VM is off as expected")

	// Power on via Redfish.
	bmc.ResetSystem("On")
	time.Sleep(3 * time.Second)

	state = bmc.GetPowerState()
	if state != "On" {
		t.Fatalf("expected PowerState=On after power on, got %q", state)
	}
	t.Log("VM powered on via Redfish")
}

// TestExample_ChassisAndManager demonstrates reading Chassis and Manager resources.
func TestExample_ChassisAndManager(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin: "qemu-system-x86_64",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "256",
			"-smp", "1",
			"-nographic",
		},
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	// Get Manager.
	mgr := bmc.GetManager()
	if mgr["ManagerType"] != "BMC" {
		t.Fatalf("expected ManagerType=BMC, got %v", mgr["ManagerType"])
	}
	t.Logf("Manager: %s (type=%s)", mgr["Name"], mgr["ManagerType"])

	// Get Chassis.
	chassis := bmc.GetChassis()
	if chassis["ChassisType"] == nil {
		t.Fatal("ChassisType missing from chassis response")
	}
	t.Logf("Chassis: %s (type=%s)", chassis["Name"], chassis["ChassisType"])
}

// TestExample_RawRedfishRequest demonstrates using the raw request helper
// for custom Redfish API interactions.
func TestExample_RawRedfishRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	bmc := bmctest.New(t, bmctest.Config{
		QEMUBin: "qemu-system-x86_64",
		QEMUArgs: []string{
			"-accel", "tcg",
			"-cpu", "qemu64",
			"-machine", "q35",
			"-m", "256",
			"-smp", "1",
			"-nographic",
		},
	})
	defer bmc.Cleanup()
	bmc.WaitReady(60 * time.Second)

	// Use the raw request helper for a GET with full response access.
	resp := bmc.RedfishRequestRaw("GET", "/redfish/v1/Systems/1", "")
	defer resp.Body.Close()

	// Check ETag header is present.
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("ETag header missing from system response")
	}
	t.Logf("System ETag: %s", etag)

	// Check Content-Type.
	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Fatalf("expected Content-Type=application/json, got %q", ct)
	}
}


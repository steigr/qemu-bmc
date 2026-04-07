package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoad_Defaults(t *testing.T) {
	// Clear any env vars that might be set
	for _, key := range []string{"QMP_SOCK", "IPMI_USER", "IPMI_PASS", "REDFISH_ADDR", "REDFISH_PORT", "IPMI_PORT", "SERIAL_ADDR", "TLS_CERT", "TLS_KEY", "VM_BOOT_MODE", "VM_IPMI_ADDR", "QEMU_BINARY", "POWER_ON_AT_START"} {
		os.Unsetenv(key)
	}

	cfg := Load()
	assert.Equal(t, "/var/run/qemu/qmp.sock", cfg.QMPSocket)
	assert.Equal(t, "admin", cfg.IPMIUser)
	assert.Equal(t, "password", cfg.IPMIPass)
	assert.Equal(t, "127.0.0.1", cfg.RedfishAddr)
	assert.Equal(t, "8080", cfg.RedfishPort)
	assert.Equal(t, "6623", cfg.IPMIPort)
	assert.Equal(t, "localhost:9002", cfg.SerialAddr)
	assert.Equal(t, "", cfg.TLSCert)
	assert.Equal(t, "", cfg.TLSKey)
	assert.False(t, cfg.UseTLS)
	assert.Equal(t, "bios", cfg.VMBootMode)
	assert.Equal(t, "", cfg.VMIPMIAddr)
	assert.Equal(t, "qemu-system-x86_64", cfg.QEMUBinary)
	assert.Equal(t, false, cfg.PowerOnAtStart)
}

func TestLoad_UseTLS_WhenBothCertAndKeyPresent(t *testing.T) {
	os.Setenv("TLS_CERT", "/tmp/tls.crt")
	os.Setenv("TLS_KEY", "/tmp/tls.key")
	defer os.Unsetenv("TLS_CERT")
	defer os.Unsetenv("TLS_KEY")

	cfg := Load()
	assert.True(t, cfg.UseTLS)
}

func TestLoad_UseTLS_FalseWhenOnlyOneIsSet(t *testing.T) {
	os.Setenv("TLS_CERT", "/tmp/tls.crt")
	os.Unsetenv("TLS_KEY")
	defer os.Unsetenv("TLS_CERT")

	cfg := Load()
	assert.False(t, cfg.UseTLS)
}

func TestLoad_QEMUBinary_Custom(t *testing.T) {
	os.Setenv("QEMU_BINARY", "/usr/bin/qemu-system-aarch64")
	defer os.Unsetenv("QEMU_BINARY")

	cfg := Load()
	assert.Equal(t, "/usr/bin/qemu-system-aarch64", cfg.QEMUBinary)
}

func TestLoad_CustomValues(t *testing.T) {
	os.Setenv("QMP_SOCK", "/tmp/test.sock")
	os.Setenv("IPMI_USER", "testuser")
	os.Setenv("IPMI_PASS", "testpass")
	defer func() {
		os.Unsetenv("QMP_SOCK")
		os.Unsetenv("IPMI_USER")
		os.Unsetenv("IPMI_PASS")
	}()

	cfg := Load()
	assert.Equal(t, "/tmp/test.sock", cfg.QMPSocket)
	assert.Equal(t, "testuser", cfg.IPMIUser)
	assert.Equal(t, "testpass", cfg.IPMIPass)
}

func TestLoad_VMIPMIAddr(t *testing.T) {
	os.Setenv("VM_IPMI_ADDR", ":9002")
	defer os.Unsetenv("VM_IPMI_ADDR")
	cfg := Load()
	assert.Equal(t, ":9002", cfg.VMIPMIAddr)
}

func TestLoad_VMIPMIAddr_Default(t *testing.T) {
	os.Unsetenv("VM_IPMI_ADDR")
	cfg := Load()
	assert.Equal(t, "", cfg.VMIPMIAddr)
}

func TestLoad_PowerOnAtStart_Default(t *testing.T) {
	os.Unsetenv("POWER_ON_AT_START")
	cfg := Load()
	assert.Equal(t, false, cfg.PowerOnAtStart)
}

func TestLoad_PowerOnAtStart_True(t *testing.T) {
	os.Setenv("POWER_ON_AT_START", "true")
	defer os.Unsetenv("POWER_ON_AT_START")
	cfg := Load()
	assert.Equal(t, true, cfg.PowerOnAtStart)
}

func TestLoad_PowerOnAtStart_False(t *testing.T) {
	os.Setenv("POWER_ON_AT_START", "false")
	defer os.Unsetenv("POWER_ON_AT_START")
	cfg := Load()
	assert.Equal(t, false, cfg.PowerOnAtStart)
}

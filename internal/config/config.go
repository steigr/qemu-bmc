package config

import "os"

// Config holds the application configuration
type Config struct {
	QMPSocket      string
	IPMIUser       string
	IPMIPass       string
	RedfishAddr    string
	RedfishPort    string
	IPMIPort       string
	SerialAddr     string
	TLSCert        string
	TLSKey         string
	UseTLS         bool
	VMBootMode     string
	VMIPMIAddr     string // VM IPMI chardev listen address
	QEMUBinary     string // QEMU binary path for process management mode
	PowerOnAtStart bool   // Power on VM at container start
	VNCAddr        string // VNC TCP address for noVNC proxy
}

// Load reads configuration from environment variables with defaults
func Load() *Config {
	tlsCert := getEnv("TLS_CERT", "")
	tlsKey := getEnv("TLS_KEY", "")

	return &Config{
		QMPSocket:      getEnv("QMP_SOCK", "/var/run/qemu/qmp.sock"),
		IPMIUser:       getEnv("IPMI_USER", "admin"),
		IPMIPass:       getEnv("IPMI_PASS", "password"),
		RedfishAddr:    getEnv("REDFISH_ADDR", "127.0.0.1"),
		RedfishPort:    getEnv("REDFISH_PORT", "8080"),
		IPMIPort:       getEnv("IPMI_PORT", "6623"),
		SerialAddr:     getEnvAllowEmpty("SERIAL_ADDR", "localhost:9002"),
		TLSCert:        tlsCert,
		TLSKey:         tlsKey,
		UseTLS:         tlsCert != "" && tlsKey != "",
		VMBootMode:     getEnv("VM_BOOT_MODE", "bios"),
		VMIPMIAddr:     getEnv("VM_IPMI_ADDR", ""),
		QEMUBinary:     getEnv("QEMU_BINARY", "qemu-system-x86_64"),
		PowerOnAtStart: getBoolEnv("POWER_ON_AT_START", false),
		VNCAddr:        getEnv("VNC_ADDR", "localhost:5900"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvAllowEmpty returns the env var value if set (even if empty), otherwise defaultValue.
func getEnvAllowEmpty(key, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

func getBoolEnv(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	switch value {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return defaultValue
	}
}

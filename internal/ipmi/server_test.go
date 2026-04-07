package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/bmc"
	"github.com/steigr/qemu-bmc/internal/machine"
)

func TestServer_HandleMessage_GetChannelAuthCaps(t *testing.T) {
	mock := newIPMIMockMachine(machine.PowerOn)
	server := NewServer(mock, bmc.NewState("admin", "password"), "admin", "password")

	// Build RMCP + IPMI 1.5 Get Channel Auth Capabilities request
	ipmiMsg := buildTestIPMIRequest(NetFnApp, CmdGetChannelAuthCapabilities, []byte{0x0e, 0x04})
	sessionWrapper := buildTestSessionWrapper(ipmiMsg)
	rmcpMsg := SerializeRMCPMessage(RMCPClassIPMI, sessionWrapper)

	resp, err := server.HandleMessage(rmcpMsg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify response is valid RMCP
	assert.Equal(t, byte(RMCPVersion1), resp[0])
	assert.Equal(t, byte(RMCPClassIPMI), resp[3])
}

func TestServer_HandleMessage_GetChassisStatus(t *testing.T) {
	mock := newIPMIMockMachine(machine.PowerOn)
	server := NewServer(mock, bmc.NewState("admin", "password"), "admin", "password")

	ipmiMsg := buildTestIPMIRequest(NetFnChassis, CmdGetChassisStatus, nil)
	sessionWrapper := buildTestSessionWrapper(ipmiMsg)
	rmcpMsg := SerializeRMCPMessage(RMCPClassIPMI, sessionWrapper)

	resp, err := server.HandleMessage(rmcpMsg)
	require.NoError(t, err)
	require.NotNil(t, resp)
}

func TestServer_HandleMessage_ChassisControl(t *testing.T) {
	mock := newIPMIMockMachine(machine.PowerOn)
	server := NewServer(mock, bmc.NewState("admin", "password"), "admin", "password")

	ipmiMsg := buildTestIPMIRequest(NetFnChassis, CmdChassisControl, []byte{ChassisControlPowerDown})
	sessionWrapper := buildTestSessionWrapper(ipmiMsg)
	rmcpMsg := SerializeRMCPMessage(RMCPClassIPMI, sessionWrapper)

	resp, err := server.HandleMessage(rmcpMsg)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Contains(t, mock.calls, "ForceOff")
}

// Helper to build a test IPMI message
func buildTestIPMIRequest(netFn uint8, cmd uint8, data []byte) []byte {
	targetAddr := uint8(0x20) // BMC
	targetLun := (netFn << 2)
	sourceAddr := uint8(0x81) // remote console
	sourceLun := uint8(0x00)

	// Header checksum
	headerSum := uint32(targetAddr) + uint32(targetLun)
	headerChecksum := uint8(0x100 - (headerSum & 0xFF))

	var buf []byte
	buf = append(buf, targetAddr, targetLun, headerChecksum)
	buf = append(buf, sourceAddr, sourceLun, cmd)
	buf = append(buf, data...)

	// Data checksum
	dataSum := uint32(sourceAddr) + uint32(sourceLun) + uint32(cmd)
	for _, b := range data {
		dataSum += uint32(b)
	}
	dataChecksum := uint8(0x100 - (dataSum & 0xFF))
	buf = append(buf, dataChecksum)

	return buf
}

// Helper to wrap in IPMI 1.5 session
func buildTestSessionWrapper(ipmiMsg []byte) []byte {
	var buf []byte
	buf = append(buf, AuthTypeNone)        // auth type
	buf = append(buf, 0, 0, 0, 0)          // sequence (little-endian)
	buf = append(buf, 0, 0, 0, 0)          // session ID (little-endian)
	buf = append(buf, byte(len(ipmiMsg)))   // message length
	buf = append(buf, ipmiMsg...)
	return buf
}

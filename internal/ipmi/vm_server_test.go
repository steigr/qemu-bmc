package ipmi

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/bmc"
	"github.com/steigr/qemu-bmc/internal/machine"
)

// vmTestHelper sets up a net.Pipe-based VMServer test environment.
// It returns the client conn, VMServer, and a function that waits for HandleConnection to return.
func vmTestHelper(t *testing.T, powerState machine.PowerState) (net.Conn, *VMServer, func() error) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	mock := newIPMIMockMachine(powerState)
	state := bmc.NewState("admin", "password")
	vs := NewVMServer(mock, state)

	var handleErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		handleErr = vs.HandleConnection(serverConn)
	}()

	waitFn := func() error {
		wg.Wait()
		return handleErr
	}

	return clientConn, vs, waitFn
}

// vmDoHandshake sends the version and capabilities commands, reads the NOATTN response.
func vmDoHandshake(t *testing.T, conn net.Conn) {
	t.Helper()

	// Send version command: [VMCmdVersion, 0x01, VMCmdChar]
	_, err := conn.Write([]byte{VMCmdVersion, 0x01, VMCmdChar})
	require.NoError(t, err)

	// Give the server time to process (version command doesn't produce a response)
	time.Sleep(10 * time.Millisecond)

	// Send capabilities command: [VMCmdCapabilities, 0x3F, VMCmdChar]
	_, err = conn.Write([]byte{VMCmdCapabilities, 0x3F, VMCmdChar})
	require.NoError(t, err)

	// Read response — should be a NOATTN command terminated by VMCmdChar
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp := make([]byte, 64)
	n, err := conn.Read(resp)
	require.NoError(t, err)
	require.Greater(t, n, 0)

	// The response should end with VMCmdChar
	assert.Equal(t, uint8(VMCmdChar), resp[n-1], "response should be terminated by VMCmdChar")

	// Reset deadline
	conn.SetReadDeadline(time.Time{})
}

// vmBuildIPMIRequest builds a raw VM IPMI request frame: escaped([seq, netfn<<2|lun, cmd, data..., checksum]) + VMMsgChar
func vmBuildIPMIRequestFrame(seq, netFn, lun, cmd uint8, data []byte) []byte {
	// Build raw request: [seq] [netfn<<2|lun] [cmd] [data...] [checksum]
	raw := []byte{seq, (netFn << 2) | (lun & 0x03), cmd}
	raw = append(raw, data...)
	checksum := vmChecksum(raw)
	raw = append(raw, checksum)

	// Escape and append terminator
	escaped := vmEscapeBytes(raw)
	escaped = append(escaped, VMMsgChar)
	return escaped
}

// vmReadResponse reads bytes from conn until VMMsgChar or VMCmdChar is seen.
// Returns the terminator byte and the unescaped data.
func vmReadResponse(t *testing.T, conn net.Conn) (byte, []byte) {
	t.Helper()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	var buf []byte
	b := make([]byte, 1)
	for {
		n, err := conn.Read(b)
		if err != nil {
			t.Fatalf("vmReadResponse: %v (read %d bytes so far: %x)", err, len(buf), buf)
		}
		if n == 0 {
			continue
		}
		if b[0] == VMMsgChar || b[0] == VMCmdChar {
			terminator := b[0]
			unescaped, err := vmUnescapeBytes(buf)
			require.NoError(t, err)
			return terminator, unescaped
		}
		buf = append(buf, b[0])
	}
}

func TestVMServer_Handshake(t *testing.T) {
	clientConn, vs, waitFn := vmTestHelper(t, machine.PowerOn)
	defer clientConn.Close()

	// Send version command
	_, err := clientConn.Write([]byte{VMCmdVersion, 0x01, VMCmdChar})
	require.NoError(t, err)

	// Small delay for server processing
	time.Sleep(10 * time.Millisecond)

	// Send capabilities command with all capabilities
	_, err = clientConn.Write([]byte{VMCmdCapabilities, 0x3F, VMCmdChar})
	require.NoError(t, err)

	// Read the NOATTN response
	terminator, data := vmReadResponse(t, clientConn)
	assert.Equal(t, uint8(VMCmdChar), terminator, "should be a control command response")

	// The response should be a NOATTN command
	cmd, _, err := vmParseControlCommand(data)
	require.NoError(t, err)
	assert.Equal(t, uint8(VMCmdNoAttn), cmd, "server should respond with NOATTN")

	// Verify capabilities were stored
	assert.Equal(t, uint8(0x3F), vs.vmCaps)

	// Close connection to trigger EOF
	clientConn.Close()

	// Wait for HandleConnection to return
	err = waitFn()
	assert.NoError(t, err)
}

func TestVMServer_GetDeviceID(t *testing.T) {
	clientConn, _, waitFn := vmTestHelper(t, machine.PowerOn)

	// Do handshake
	vmDoHandshake(t, clientConn)

	// Build Get Device ID request: seq=1, netfn=App(0x06), lun=0, cmd=0x01
	frame := vmBuildIPMIRequestFrame(0x01, NetFnApp, 0x00, CmdGetDeviceID, nil)
	_, err := clientConn.Write(frame)
	require.NoError(t, err)

	// Read IPMI response
	terminator, data := vmReadResponse(t, clientConn)
	assert.Equal(t, uint8(VMMsgChar), terminator, "should be an IPMI message response")

	// Parse the response: [seq] [netfn<<2|lun] [cmd] [cc] [data...] [checksum]
	require.True(t, len(data) >= 5, "response should be at least 5 bytes: seq+netfn/lun+cmd+cc+checksum")

	// Verify checksum: sum of all bytes mod 256 should be 0
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	assert.Equal(t, uint8(0x00), uint8(sum&0xFF), "response checksum should validate")

	// Verify fields
	assert.Equal(t, uint8(0x01), data[0], "seq should echo request seq")
	respNetFn := (data[1] >> 2) & 0x3F
	assert.Equal(t, uint8(NetFnAppResponse), respNetFn, "netfn should be App response (0x07)")
	assert.Equal(t, uint8(CmdGetDeviceID), data[2], "cmd should echo request cmd")
	assert.Equal(t, uint8(CompletionCodeOK), data[3], "completion code should be OK")

	// Close and wait
	clientConn.Close()
	err = waitFn()
	assert.NoError(t, err)
}

func TestVMServer_GetUserName(t *testing.T) {
	clientConn, _, waitFn := vmTestHelper(t, machine.PowerOn)

	// Do handshake
	vmDoHandshake(t, clientConn)

	// Build Get User Name request: seq=2, netfn=App(0x06), cmd=CmdGetUserName(0x46), data=[0x02] (user ID 2)
	frame := vmBuildIPMIRequestFrame(0x02, NetFnApp, 0x00, CmdGetUserName, []byte{0x02})
	_, err := clientConn.Write(frame)
	require.NoError(t, err)

	// Read IPMI response
	terminator, data := vmReadResponse(t, clientConn)
	assert.Equal(t, uint8(VMMsgChar), terminator, "should be an IPMI message response")

	// Parse response
	require.True(t, len(data) >= 5, "response should have at least 5 bytes")

	// Verify checksum
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	assert.Equal(t, uint8(0x00), uint8(sum&0xFF), "checksum should validate")

	// Verify fields
	assert.Equal(t, uint8(0x02), data[0], "seq should echo")
	assert.Equal(t, uint8(CmdGetUserName), data[2], "cmd should echo")
	assert.Equal(t, uint8(CompletionCodeOK), data[3], "cc should be OK")

	// The response data (between cc and checksum) should contain "admin"
	if len(data) > 5 {
		respPayload := data[4 : len(data)-1] // everything between cc and checksum
		assert.Equal(t, 16, len(respPayload), "username response should be 16 bytes")
		assert.Equal(t, "admin", string(respPayload[:5]), "username should be admin")
	} else {
		t.Fatal("response too short to contain username data")
	}

	// Close and wait
	clientConn.Close()
	err = waitFn()
	assert.NoError(t, err)
}

func TestVMServer_SetBootOptions(t *testing.T) {
	clientConn, vs, waitFn := vmTestHelper(t, machine.PowerOn)

	// Do handshake
	vmDoHandshake(t, clientConn)

	// Build Set Boot Options request for PXE boot:
	// Parameter selector 5 (boot flags), valid=1 (bit7), UEFI=1 (bit5), device=PXE(0x01 in bits 5:2 = 0x04)
	// Data: [param_selector=0x05] [valid|uefi=0xA0] [device=0x04] [0x00] [0x00] [0x00]
	bootData := []byte{0x05, 0xA0, 0x04, 0x00, 0x00, 0x00}
	frame := vmBuildIPMIRequestFrame(0x03, NetFnChassis, 0x00, CmdSetBootOptions, bootData)
	_, err := clientConn.Write(frame)
	require.NoError(t, err)

	// Read IPMI response
	terminator, data := vmReadResponse(t, clientConn)
	assert.Equal(t, uint8(VMMsgChar), terminator, "should be an IPMI message response")

	// Parse response
	require.True(t, len(data) >= 5, "response should have at least 5 bytes")

	// Verify checksum
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	assert.Equal(t, uint8(0x00), uint8(sum&0xFF), "checksum should validate")

	// Verify fields
	assert.Equal(t, uint8(0x03), data[0], "seq should echo")
	assert.Equal(t, uint8(CmdSetBootOptions), data[2], "cmd should echo")
	assert.Equal(t, uint8(CompletionCodeOK), data[3], "cc should be OK")

	// Verify the mock machine's boot override was updated
	// Access the machine through VMServer (it's the mock we passed in)
	mockMachine := vs.machine.(*ipmiMockMachine)
	boot := mockMachine.GetBootOverride()
	assert.Equal(t, "Once", boot.Enabled, "boot should be enabled once")
	assert.Equal(t, "Pxe", boot.Target, "boot target should be PXE")
	assert.Equal(t, "UEFI", boot.Mode, "boot mode should be UEFI")

	// Close and wait
	clientConn.Close()
	err = waitFn()
	assert.NoError(t, err)
}

func TestVMServer_ConnectionEOF(t *testing.T) {
	clientConn, _, waitFn := vmTestHelper(t, machine.PowerOn)

	// Close immediately — HandleConnection should return nil on EOF
	clientConn.Close()

	err := waitFn()
	assert.NoError(t, err)
}

func TestVMReader_ReadMessage(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	reader := &vmReader{conn: serverConn}

	// Write a control command message from client
	go func() {
		clientConn.Write([]byte{VMCmdVersion, 0x01, VMCmdChar})
	}()

	serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	terminator, data, err := reader.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, uint8(VMCmdChar), terminator)
	assert.Equal(t, []byte{VMCmdVersion, 0x01}, data)
}

func TestVMReader_ReadMessage_Escaped(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	reader := &vmReader{conn: serverConn}

	// Write escaped data: 0xAA 0xB0 should unescape to 0xA0
	go func() {
		clientConn.Write([]byte{0x01, VMEscapeChar, 0xB0, 0x02, VMMsgChar})
	}()

	serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	terminator, data, err := reader.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, uint8(VMMsgChar), terminator)
	assert.Equal(t, []byte{0x01, 0xA0, 0x02}, data)
}

func TestVMReader_ReadMessage_EOF(t *testing.T) {
	clientConn, serverConn := net.Pipe()

	reader := &vmReader{conn: serverConn}

	// Close client connection to trigger EOF
	clientConn.Close()

	_, _, err := reader.ReadMessage()
	assert.ErrorIs(t, err, io.EOF)
}

package ipmi

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/bmc"
	"github.com/steigr/qemu-bmc/internal/machine"
)

func TestOpenSession(t *testing.T) {
	sm := NewSessionManager()

	// Build Open Session Request
	req := buildOpenSessionRequest(0x01, 0x12345678)
	data := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, req)

	resp, err := HandleRMCPPlusMessage(data, sm, "admin", "password", nil, bmc.NewState("admin", "password"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Parse response - skip RMCP+ header (12 bytes)
	assert.Equal(t, uint8(AuthTypeRMCPPlus), resp[0])
	assert.Equal(t, uint8(PayloadTypeOpenSessionResponse), resp[1])

	// Check status code (byte 13 = message tag, byte 14 = status)
	assert.Equal(t, uint8(0x01), resp[12]) // message tag echoed
	assert.Equal(t, uint8(0x00), resp[13]) // success
}

func TestRAKPAuthentication(t *testing.T) {
	sm := NewSessionManager()
	user := "admin"
	pass := "password"

	// Step 1: Open Session
	openReq := buildOpenSessionRequest(0x01, 0xAAAABBBB)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// Extract ManagedSystemSessionID from response (bytes 20-23)
	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// Step 2: RAKP Message 1
	rakp1 := buildRAKPMessage1(0x02, managedSessionID, user)
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1)
	rakp2Resp, err := HandleRMCPPlusMessage(rakp1Data, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// Check RAKP2 status (byte 13)
	assert.Equal(t, uint8(0x00), rakp2Resp[13], "RAKP2 should be success")

	// Extract server random and GUID from RAKP2
	serverRandom := rakp2Resp[20:36]
	serverGUID := rakp2Resp[36:52]

	// Step 3: Build and send RAKP Message 3
	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)

	rakp3AuthBuf := buildRAKP3AuthBuf(session.ManagedSystemRandomNumber[:], session.RemoteConsoleSessionID, session.RequestedPrivilegeLevel, session.UserNameLength, session.UserName)
	mac := hmac.New(sha1.New, []byte(pass))
	mac.Write(rakp3AuthBuf)
	rakp3AuthCode := mac.Sum(nil)

	rakp3 := buildRAKPMessage3(0x03, managedSessionID, rakp3AuthCode)
	rakp3Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, rakp3)
	rakp4Resp, err := HandleRMCPPlusMessage(rakp3Data, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// Check RAKP4 status (byte 13)
	assert.Equal(t, uint8(0x00), rakp4Resp[13], "RAKP4 should be success")

	// Verify session is authenticated
	assert.True(t, session.Authenticated)
	assert.NotNil(t, session.SessionIntegrityKey)
	assert.NotNil(t, session.IntegrityKey)
	assert.NotNil(t, session.ConfidentialityKey)

	_ = serverRandom
	_ = serverGUID
}

func TestRAKPAuthentication_WrongPassword(t *testing.T) {
	sm := NewSessionManager()
	user := "admin"
	pass := "password"

	// Open Session
	openReq := buildOpenSessionRequest(0x01, 0xAAAABBBB)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// RAKP1
	rakp1 := buildRAKPMessage1(0x02, managedSessionID, user)
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1)
	_, err = HandleRMCPPlusMessage(rakp1Data, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// RAKP3 with wrong auth code (simulating wrong password)
	wrongAuthCode := make([]byte, 20)
	rakp3 := buildRAKPMessage3(0x03, managedSessionID, wrongAuthCode)
	rakp3Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, rakp3)
	rakp4Resp, err := HandleRMCPPlusMessage(rakp3Data, sm, user, pass, nil, bmc.NewState(user, pass))
	require.NoError(t, err)

	// RAKP4 should indicate failure (status != 0x00)
	assert.NotEqual(t, uint8(0x00), rakp4Resp[13], "RAKP4 should fail with wrong password")
}

func TestRAKP_AuthWithBMCStateUser(t *testing.T) {
	sm := NewSessionManager()
	state := bmc.NewState("admin", "password")

	// Create a new user via BMC state (simulating in-band creation)
	state.SetUserName(3, "maas")
	state.SetUserPassword(3, "maas-secret")
	state.SetUserAccess(1, 3, bmc.UserAccess{Enabled: true, PrivilegeLimit: 4, IPMIMessaging: true, LinkAuth: true})

	// Step 1: Open Session
	openReq := buildOpenSessionRequest(0x01, 0x12345678)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// Extract managed system session ID from response (bytes 20-23, after 12-byte header)
	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// Step 2: RAKP Message 1 with "maas" user
	rakp1Req := buildRAKPMessage1(0x02, managedSessionID, "maas")
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1Req)
	rakp2Resp, err := HandleRMCPPlusMessage(rakp1Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// Check RAKP2 status = success (byte 13)
	assert.Equal(t, uint8(0x00), rakp2Resp[13], "RAKP2 should succeed for BMC state user")

	// Step 3: RAKP Message 3 (compute proper auth code with "maas-secret")
	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)

	rakp3AuthBuf := buildRAKP3AuthBuf(session.ManagedSystemRandomNumber[:], session.RemoteConsoleSessionID, session.RequestedPrivilegeLevel, session.UserNameLength, session.UserName)
	mac := hmac.New(sha1.New, []byte("maas-secret"))
	mac.Write(rakp3AuthBuf)
	rakp3AuthCode := mac.Sum(nil)

	rakp3 := buildRAKPMessage3(0x03, managedSessionID, rakp3AuthCode)
	rakp3Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, rakp3)
	rakp4Resp, err := HandleRMCPPlusMessage(rakp3Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// Check RAKP4 status = success (byte 13)
	assert.Equal(t, uint8(0x00), rakp4Resp[13], "RAKP4 should succeed for BMC state user")

	// Verify session is authenticated
	assert.True(t, session.Authenticated)
	assert.NotNil(t, session.SessionIntegrityKey)
	assert.NotNil(t, session.IntegrityKey)
	assert.NotNil(t, session.ConfidentialityKey)
}

func TestRAKP_AuthWithBMCStateUser_WrongPassword(t *testing.T) {
	sm := NewSessionManager()
	state := bmc.NewState("admin", "password")

	// Create a new user via BMC state
	state.SetUserName(3, "maas")
	state.SetUserPassword(3, "maas-secret")
	state.SetUserAccess(1, 3, bmc.UserAccess{Enabled: true, PrivilegeLimit: 4, IPMIMessaging: true, LinkAuth: true})

	// Open Session
	openReq := buildOpenSessionRequest(0x01, 0x12345678)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// RAKP1 with "maas" user
	rakp1Req := buildRAKPMessage1(0x02, managedSessionID, "maas")
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1Req)
	_, err = HandleRMCPPlusMessage(rakp1Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// RAKP3 with wrong password ("wrong-password" instead of "maas-secret")
	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)

	rakp3AuthBuf := buildRAKP3AuthBuf(session.ManagedSystemRandomNumber[:], session.RemoteConsoleSessionID, session.RequestedPrivilegeLevel, session.UserNameLength, session.UserName)
	mac := hmac.New(sha1.New, []byte("wrong-password"))
	mac.Write(rakp3AuthBuf)
	rakp3AuthCode := mac.Sum(nil)

	rakp3 := buildRAKPMessage3(0x03, managedSessionID, rakp3AuthCode)
	rakp3Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, rakp3)
	rakp4Resp, err := HandleRMCPPlusMessage(rakp3Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// RAKP4 should indicate failure
	assert.NotEqual(t, uint8(0x00), rakp4Resp[13], "RAKP4 should fail with wrong password for BMC state user")
}

func TestRAKP_AuthWithUnknownUser(t *testing.T) {
	sm := NewSessionManager()
	state := bmc.NewState("admin", "password")

	// Open Session
	openReq := buildOpenSessionRequest(0x01, 0x12345678)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	// RAKP1 with unknown user (not in BMC state, not hardcoded)
	rakp1Req := buildRAKPMessage1(0x02, managedSessionID, "unknown")
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1Req)
	rakp2Resp, err := HandleRMCPPlusMessage(rakp1Data, sm, "admin", "password", nil, state)
	require.NoError(t, err)

	// RAKP2 should indicate invalid username (0x0D)
	assert.Equal(t, uint8(0x0D), rakp2Resp[13], "RAKP2 should fail with invalid username")
}

func TestOpenSession_CipherSuite3_AlgorithmsStoredInSession(t *testing.T) {
	sm := NewSessionManager()

	req := buildOpenSessionRequest(0x01, 0x12345678)
	data := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, req)

	resp, err := HandleRMCPPlusMessage(data, sm, "admin", "password", nil, bmc.NewState("admin", "password"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Status must be success
	assert.Equal(t, uint8(0x00), resp[13])

	// Extract managed system session ID from response and verify session algorithms
	managedSessionID := binary.LittleEndian.Uint32(resp[20:24])
	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)
	assert.Equal(t, uint8(AuthAlgorithmHMACSHA1), session.AuthAlgorithm)
	assert.Equal(t, uint8(IntegrityAlgorithmHMACSHA1_96), session.IntegrityAlgorithm)
	assert.Equal(t, uint8(ConfAlgorithmAESCBC128), session.ConfidentialityAlgorithm)

	// Verify response payload contains BMC-chosen algorithm values.
	// Full response layout (from byte 0):
	//   [0-11]  RMCP+ session header (AuthType, PayloadType, SessionID×2, SeqNum, Length)
	//   [12]    MessageTag
	//   [13]    Status
	//   [14]    MaxPrivilege
	//   [15]    Reserved
	//   [16-19] RemoteConsoleSessionID
	//   [20-23] ManagedSystemSessionID
	//   [24-31] Auth algorithm payload:  Type[1] + Reserved[2] + Len[1] + Algorithm[1] + Reserved[3]
	//   [32-39] Integrity algorithm payload (same structure)
	//   [40-47] Confidentiality algorithm payload (same structure)
	const (
		authAlgOffset  = 24 + 4
		intAlgOffset   = 32 + 4
		confAlgOffset  = 40 + 4
	)
	assert.Equal(t, uint8(AuthAlgorithmHMACSHA1), resp[authAlgOffset], "auth algorithm in response")
	assert.Equal(t, uint8(IntegrityAlgorithmHMACSHA1_96), resp[intAlgOffset], "integrity algorithm in response")
	assert.Equal(t, uint8(ConfAlgorithmAESCBC128), resp[confAlgOffset], "conf algorithm in response")
}

func TestOpenSession_UnsupportedAuthAlgorithm(t *testing.T) {
	sm := NewSessionManager()

	// Cipher suite 0: Auth=None (0x00)
	req := buildOpenSessionRequestWithAlgorithms(0x01, 0x12345678, AuthAlgorithmNone, IntegrityAlgorithmNone, ConfAlgorithmNone)
	data := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, req)

	resp, err := HandleRMCPPlusMessage(data, sm, "admin", "password", nil, bmc.NewState("admin", "password"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, uint8(PayloadTypeOpenSessionResponse), resp[1])
	assert.Equal(t, uint8(OpenSessionStatusInvalidAuthAlgorithm), resp[13], "should reject unsupported auth algorithm")
}

func TestOpenSession_UnsupportedIntegrityAlgorithm(t *testing.T) {
	sm := NewSessionManager()

	// Cipher suite 1: Auth=HMAC-SHA1 but Integrity=None → only partial support
	req := buildOpenSessionRequestWithAlgorithms(0x01, 0x12345678, AuthAlgorithmHMACSHA1, IntegrityAlgorithmNone, ConfAlgorithmNone)
	data := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, req)

	resp, err := HandleRMCPPlusMessage(data, sm, "admin", "password", nil, bmc.NewState("admin", "password"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, uint8(PayloadTypeOpenSessionResponse), resp[1])
	assert.Equal(t, uint8(OpenSessionStatusInvalidIntegrityAlgorithm), resp[13], "should reject unsupported integrity algorithm")
}

func TestOpenSession_UnsupportedConfAlgorithm(t *testing.T) {
	sm := NewSessionManager()

	// Cipher suite 2: Auth=HMAC-SHA1, Integrity=HMAC-SHA1-96, Conf=None
	req := buildOpenSessionRequestWithAlgorithms(0x01, 0x12345678, AuthAlgorithmHMACSHA1, IntegrityAlgorithmHMACSHA1_96, ConfAlgorithmNone)
	data := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, req)

	resp, err := HandleRMCPPlusMessage(data, sm, "admin", "password", nil, bmc.NewState("admin", "password"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, uint8(PayloadTypeOpenSessionResponse), resp[1])
	assert.Equal(t, uint8(OpenSessionStatusInvalidConfAlgorithm), resp[13], "should reject unsupported conf algorithm")
}

func TestOpenSession_UnsupportedCipherSuite17(t *testing.T) {
	sm := NewSessionManager()

	// Cipher suite 17: RAKP-HMAC-SHA256 (0x03) + HMAC-SHA256-128 (0x04) + AES-CBC-128 (0x01)
	req := buildOpenSessionRequestWithAlgorithms(0x01, 0x12345678, AuthAlgorithmHMACSHA256, IntegrityAlgorithmHMACSHA256_128, ConfAlgorithmAESCBC128)
	data := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, req)

	resp, err := HandleRMCPPlusMessage(data, sm, "admin", "password", nil, bmc.NewState("admin", "password"))
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, uint8(PayloadTypeOpenSessionResponse), resp[1])
	assert.Equal(t, uint8(OpenSessionStatusInvalidAuthAlgorithm), resp[13], "should reject SHA256 auth algorithm")
}

func TestEncryptDecryptAESCBC(t *testing.T) {
	key := make([]byte, 20)
	for i := range key {
		key[i] = byte(i)
	}

	plaintext := []byte("Hello, IPMI World!")
	encrypted, err := encryptAESCBC(key, plaintext)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, encrypted)

	decrypted, err := decryptAESCBC(key, encrypted)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestPadUnpad(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5}
	padded := padPayload(data)
	assert.Equal(t, 0, len(padded)%16, "padded data should be aligned to 16 bytes")

	unpadded, err := unpadPayload(padded)
	require.NoError(t, err)
	assert.Equal(t, data, unpadded)
}

// TestPadPayload_IPMISpec verifies that padPayload produces IPMI 2.0 spec-compliant
// padding where the last byte is CPL (Confidentiality Pad Length = count of CPad bytes,
// i.e. padSize-1), NOT the total pad byte count.
// IPMI 2.0 spec §13.28.3: trailer = [01h, 02h, ..., CPLh, CPL] where CPL = padSize-1.
func TestPadPayload_IPMISpec(t *testing.T) {
	cases := []struct {
		dataLen int
		padSize int
	}{
		{7, 9},   // 7-byte IPMI msg: padSize=9, CPL=8, trailer=[1..8,8]
		{8, 8},   // 8-byte IPMI msg: padSize=8, CPL=7, trailer=[1..7,7]
		{16, 16}, // 16-byte aligned: padSize=16, CPL=15, trailer=[1..15,15]
	}
	for _, tc := range cases {
		data := make([]byte, tc.dataLen)
		padded := padPayload(data)
		require.Equal(t, tc.dataLen+tc.padSize, len(padded), "padded length for dataLen=%d", tc.dataLen)
		// Last byte must equal CPL = padSize-1 (IPMI 2.0 spec §13.28.3)
		lastByte := padded[len(padded)-1]
		assert.Equal(t, byte(tc.padSize-1), lastByte,
			"last pad byte must be CPL=%d (padSize-1), got %d for dataLen=%d",
			tc.padSize-1, lastByte, tc.dataLen)
	}
}

// TestUnpadPayload_IPMISpec verifies that unpadPayload correctly strips IPMI 2.0
// spec-compliant CPL-format padding as specified in §13.28.3 and sent by FreeIPMI.
func TestUnpadPayload_IPMISpec(t *testing.T) {
	// 7-byte IPMI message padded to 16 bytes (padSize=9):
	// CPad=[01..08] (8 bytes) + CPL=08 (1 byte) = 9 bytes total trailer.
	ipmiMsg := make([]byte, 7)
	cplPadding := []byte{1, 2, 3, 4, 5, 6, 7, 8, 8} // CPad=[1..8], CPL=8
	padded := append(ipmiMsg, cplPadding...)

	unpadded, err := unpadPayload(padded)
	require.NoError(t, err, "unpadPayload must not error on IPMI spec-compliant CPL padding")
	assert.Equal(t, 7, len(unpadded), "must recover exactly 7 bytes after stripping 9-byte CPL trailer")
}

// TestDecryptAESCBC_FreeIPMIPayload verifies end-to-end decryption of an IPMI message
// encrypted with IPMI 2.0 CPL-format AES-CBC padding (§13.28.3, as FreeIPMI produces).
func TestDecryptAESCBC_FreeIPMIPayload(t *testing.T) {
	key := make([]byte, 20)
	for i := range key {
		key[i] = byte(i + 1)
	}

	// Build a 7-byte IPMI message padded using IPMI 2.0 CPL format:
	// CPad=[01..08] + CPL=08 = 9 bytes, making 16 bytes total (1 AES block).
	ipmiMsg := []byte{0x20, 0x00, 0xE0, 0x81, 0x00, 0x01, 0x7E} // 7 bytes
	cplPadding := []byte{1, 2, 3, 4, 5, 6, 7, 8, 8}              // CPad=[1..8], CPL=8
	plaintext := append(ipmiMsg, cplPadding...)                    // 16 bytes = 1 AES block

	// Encrypt with zero IV for deterministic test
	iv := make([]byte, 16)
	block, err := aes.NewCipher(key[:16])
	require.NoError(t, err)
	ciphertext := make([]byte, 16)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plaintext)
	encrypted := append(iv, ciphertext...)

	decrypted, err := decryptAESCBC(key, encrypted)
	require.NoError(t, err, "decryptAESCBC must succeed with IPMI 2.0 CPL padding")
	assert.Equal(t, ipmiMsg, decrypted, "decrypted payload must match original 7-byte IPMI message")
}

// TestHandleEncryptedIPMI_GetChassisStatus verifies that an encrypted Get Chassis Status
// command (7-byte IPMI message, as sent by ipmi-config --driver-type LAN_2_0) receives
// a proper response.  This is the regression test for the MAAS commissioning timeout bug.
//
// The key scenario: ipmi-config --driver-type LAN_2_0 sends RMCP+ encrypted IPMI commands
// using IPMI 2.0 spec-compliant AES-CBC padding (last pad byte = count of pad bytes).
// qemu-bmc must correctly decrypt these and return a response; previously it returned nil
// (causing a session timeout on the client side).
func TestHandleEncryptedIPMI_GetChassisStatus(t *testing.T) {
	sm := NewSessionManager()
	user := "admin"
	pass := "password"
	mock := newIPMIMockMachine(machine.PowerOn)
	state := bmc.NewState(user, pass)

	// Step 1: Establish RMCP+ session
	managedSessionID := setupRMCPSession(t, sm, user, pass, state)
	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)

	// Step 2: Build Get Chassis Status IPMI message (7 bytes, no data payload)
	ipmiMsg := buildTestIPMIRequest(NetFnChassis, CmdGetChassisStatus, nil)
	require.Equal(t, 7, len(ipmiMsg), "Get Chassis Status must be 7 bytes")

	// Step 3: Encrypt using IPMI 2.0 spec-compliant padding + AES-CBC.
	// This simulates what FreeIPMI (ipmi-config --driver-type LAN_2_0) sends.
	// IPMI spec padding: [1, 2, ..., N] where N = padSize = total pad byte count.
	encryptedPayload, err := encryptIPMISpecAESCBC(session.ConfidentialityKey, ipmiMsg)
	require.NoError(t, err)

	// Step 4: Build authenticated RMCP+ packet
	pktData := buildAuthenticatedRMCPPlusPacket(session, encryptedPayload)

	// Step 5: Send to HandleRMCPPlusMessage and verify we get a response
	resp, err := HandleRMCPPlusMessage(pktData, sm, user, pass, mock, state)
	require.NoError(t, err, "encrypted Get Chassis Status must not return an error")
	require.NotNil(t, resp, "encrypted Get Chassis Status must return a response (not nil)")
}

// TestResponseSequenceNumber verifies that BMC responses carry an incrementing
// session_sequence_number (1, 2, 3, ...) as required by IPMI 2.0 spec §13.28.6.
// A response with session_sequence_number=0 is rejected by FreeIPMI as "out of window",
// causing it to retry indefinitely until session timeout — the MAAS commissioning bug.
func TestResponseSequenceNumber(t *testing.T) {
	sm := NewSessionManager()
	user := "admin"
	pass := "password"
	mock := newIPMIMockMachine(machine.PowerOn)
	state := bmc.NewState(user, pass)

	managedSessionID := setupRMCPSession(t, sm, user, pass, state)
	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)

	for i := 1; i <= 3; i++ {
		ipmiMsg := buildTestIPMIRequest(NetFnApp, CmdGetDeviceID, nil)
		enc, err := encryptIPMISpecAESCBC(session.ConfidentialityKey, ipmiMsg)
		require.NoError(t, err)

		pkt := buildAuthenticatedRMCPPlusPacket(session, enc)
		resp, err := HandleRMCPPlusMessage(pkt, sm, user, pass, mock, state)
		require.NoError(t, err)
		require.NotNil(t, resp)

		// RMCP+ session header: AuthType(1)+PayloadType(1)+SessionID(4)+SeqNum(4)+PayloadLen(2)
		// seq is at bytes 6-9
		require.GreaterOrEqual(t, len(resp), 10, "response too short to read seq")
		seqNum := binary.LittleEndian.Uint32(resp[6:10])
		assert.Equal(t, uint32(i), seqNum,
			"response #%d must have session_sequence_number=%d (not 0)", i, i)
	}
}

// encryptIPMISpecAESCBC encrypts plaintext using AES-CBC-128 with IPMI 2.0 CPL-format
// padding (§13.28.3): CPad=[01h..CPLh] + CPL byte, where CPL = padSize-1.
// This matches the encryption used by FreeIPMI clients.
func encryptIPMISpecAESCBC(key []byte, plaintext []byte) ([]byte, error) {
	padSize := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padding := make([]byte, padSize)
	for i := 0; i < padSize; i++ {
		padding[i] = byte(i + 1) // CPad bytes: 1, 2, ..., padSize
	}
	// Last byte is CPL = count of CPad bytes only (not counting CPL itself)
	padding[padSize-1] = byte(padSize - 1) // CPL = padSize - 1
	padded := append(plaintext, padding...)

	block, err := aes.NewCipher(key[:16])
	if err != nil {
		return nil, err
	}

	iv := make([]byte, aes.BlockSize) // zero IV for deterministic test
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	return append(iv, ciphertext...), nil
}

// setupRMCPSession performs Open Session + RAKP 1-4 and returns the managed system session ID.
func setupRMCPSession(t *testing.T, sm *SessionManager, user, pass string, state *bmc.State) uint32 {
	t.Helper()

	openReq := buildOpenSessionRequest(0x01, 0xAAAABBBB)
	openData := wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, openReq)
	openResp, err := HandleRMCPPlusMessage(openData, sm, user, pass, nil, state)
	require.NoError(t, err)

	managedSessionID := binary.LittleEndian.Uint32(openResp[20:24])

	rakp1 := buildRAKPMessage1(0x02, managedSessionID, user)
	rakp1Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage1, 0, 0, rakp1)
	_, err = HandleRMCPPlusMessage(rakp1Data, sm, user, pass, nil, state)
	require.NoError(t, err)

	session, ok := sm.GetSession(managedSessionID)
	require.True(t, ok)

	rakp3AuthBuf := buildRAKP3AuthBuf(session.ManagedSystemRandomNumber[:], session.RemoteConsoleSessionID, session.RequestedPrivilegeLevel, session.UserNameLength, session.UserName)
	mac := hmac.New(sha1.New, []byte(pass))
	mac.Write(rakp3AuthBuf)

	rakp3 := buildRAKPMessage3(0x03, managedSessionID, mac.Sum(nil))
	rakp3Data := wrapRMCPPlusPayload(PayloadTypeRAKPMessage3, 0, 0, rakp3)
	_, err = HandleRMCPPlusMessage(rakp3Data, sm, user, pass, nil, state)
	require.NoError(t, err)

	return managedSessionID
}

// buildAuthenticatedRMCPPlusPacket builds an authenticated+encrypted RMCP+ IPMI data packet.
func buildAuthenticatedRMCPPlusPacket(session *Session, encryptedPayload []byte) []byte {
	// Session header: AuthType + PayloadType(0xC0 = encrypted+authenticated+IPMI) + SessionID + SeqNum + PayloadLength
	payloadType := uint8(0xC0) // encrypted=1, authenticated=1, type=IPMI(0x00)
	seqNum := uint32(1)

	var hdr [12]byte
	hdr[0] = AuthTypeRMCPPlus
	hdr[1] = payloadType
	binary.LittleEndian.PutUint32(hdr[2:], session.ManagedSystemSessionID)
	binary.LittleEndian.PutUint32(hdr[6:], seqNum)
	binary.LittleEndian.PutUint16(hdr[10:], uint16(len(encryptedPayload)))

	pkt := append(hdr[:], encryptedPayload...)

	// Integrity padding (align to 4-byte boundary)
	padNeeded := (4 - ((len(pkt) + 1 + 1) % 4)) % 4
	for i := 0; i < padNeeded; i++ {
		pkt = append(pkt, 0xFF)
	}
	pkt = append(pkt, byte(padNeeded)) // Pad Length
	pkt = append(pkt, 0x07)             // Next Header

	// HMAC-SHA1-96 over the entire packet so far
	mac := hmac.New(sha1.New, session.IntegrityKey)
	mac.Write(pkt)
	pkt = append(pkt, mac.Sum(nil)[:12]...)

	return pkt
}

// Helper functions for building test messages

func wrapRMCPPlusPayload(payloadType uint8, sessionID uint32, seq uint32, payload []byte) []byte {
	buf := make([]byte, 12+len(payload))
	buf[0] = AuthTypeRMCPPlus
	buf[1] = payloadType
	binary.LittleEndian.PutUint32(buf[2:], sessionID)
	binary.LittleEndian.PutUint32(buf[6:], seq)
	binary.LittleEndian.PutUint16(buf[10:], uint16(len(payload)))
	copy(buf[12:], payload)
	return buf
}

func buildOpenSessionRequest(tag uint8, remoteSessionID uint32) []byte {
	req := make([]byte, 32)
	req[0] = tag  // message tag
	req[1] = 0x04 // max privilege (admin)
	binary.LittleEndian.PutUint32(req[4:], remoteSessionID)
	// Auth payload
	req[8] = 0x00  // type
	req[11] = 0x08 // length
	req[12] = 0x01 // RAKP-HMAC-SHA1
	// Integrity payload
	req[16] = 0x01
	req[19] = 0x08
	req[20] = 0x01 // HMAC-SHA1-96
	// Confidentiality payload
	req[24] = 0x02
	req[27] = 0x08
	req[28] = 0x01 // AES-CBC-128
	return req
}

// buildOpenSessionRequestWithAlgorithms builds an Open Session Request with explicit algorithm values.
func buildOpenSessionRequestWithAlgorithms(tag uint8, remoteSessionID uint32, authAlg, intAlg, confAlg uint8) []byte {
	req := make([]byte, 32)
	req[0] = tag
	req[1] = 0x04 // max privilege (admin)
	binary.LittleEndian.PutUint32(req[4:], remoteSessionID)
	// Auth payload
	req[8] = 0x00  // type
	req[11] = 0x08 // length
	req[12] = authAlg
	// Integrity payload
	req[16] = 0x01
	req[19] = 0x08
	req[20] = intAlg
	// Confidentiality payload
	req[24] = 0x02
	req[27] = 0x08
	req[28] = confAlg
	return req
}

func buildRAKPMessage1(tag uint8, managedSessionID uint32, user string) []byte {
	buf := make([]byte, 48)
	buf[0] = tag
	binary.LittleEndian.PutUint32(buf[4:], managedSessionID)
	// Random number at bytes 8-23
	for i := 8; i < 24; i++ {
		buf[i] = byte(i)
	}
	buf[24] = 0x04 // privilege level (admin)
	buf[27] = byte(len(user))
	copy(buf[28:], []byte(user))
	return buf
}

func buildRAKP3AuthBuf(managedSystemRandom []byte, remoteConsoleSessionID uint32, privilegeLevel uint8, userNameLength uint8, userName []byte) []byte {
	buf := make([]byte, 0, 64)
	buf = append(buf, managedSystemRandom...)
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, remoteConsoleSessionID)
	buf = append(buf, b...)
	buf = append(buf, privilegeLevel)
	buf = append(buf, userNameLength)
	buf = append(buf, userName...)
	return buf
}

func buildRAKPMessage3(tag uint8, managedSessionID uint32, authCode []byte) []byte {
	buf := make([]byte, 28)
	buf[0] = tag
	buf[1] = 0x00 // status
	binary.LittleEndian.PutUint32(buf[4:], managedSessionID)
	copy(buf[8:], authCode[:20])
	return buf
}

// Mock machine for IPMI tests
type ipmiMockMachine struct {
	powerState   machine.PowerState
	calls        []string
	bootOverride machine.BootOverride
}

func newIPMIMockMachine(state machine.PowerState) *ipmiMockMachine {
	return &ipmiMockMachine{
		powerState: state,
		bootOverride: machine.BootOverride{
			Enabled: "Disabled",
			Target:  "None",
			Mode:    "UEFI",
		},
	}
}

func (m *ipmiMockMachine) GetPowerState() (machine.PowerState, error) {
	return m.powerState, nil
}
func (m *ipmiMockMachine) Reset(resetType string) error {
	m.calls = append(m.calls, resetType)
	if resetType == "ForceOff" || resetType == "GracefulShutdown" {
		m.powerState = machine.PowerOff
	} else if resetType == "On" {
		m.powerState = machine.PowerOn
	}
	return nil
}
func (m *ipmiMockMachine) GetBootOverride() machine.BootOverride {
	return m.bootOverride
}
func (m *ipmiMockMachine) SetBootOverride(override machine.BootOverride) error {
	m.bootOverride = override
	return nil
}

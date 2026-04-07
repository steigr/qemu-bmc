package ipmi

import (
	"crypto/rand"
	"encoding/binary"

	"github.com/steigr/qemu-bmc/internal/bmc"
)

// ipmi15Session tracks IPMI 1.5 session state
var ipmi15TempSessionID uint32
var ipmi15Challenge [16]byte
var ipmi15ActiveSessionID uint32

// handleAppCommand handles Application network function commands
func handleAppCommand(msg *IPMIMessage, machine MachineInterface, state *bmc.State) (CompletionCode, []byte) {
	switch msg.Command {
	case CmdGetDeviceID:
		return handleGetDeviceID()
	case CmdGetChannelAuthCapabilities:
		return handleGetChannelAuthCapabilities(msg.Data)
	case CmdGetSessionChallenge:
		return handleGetSessionChallenge(msg.Data)
	case CmdActivateSession:
		return handleActivateSession(msg.Data)
	case CmdSetSessionPrivilege:
		return handleSetSessionPrivilege(msg.Data)
	case CmdCloseSession:
		return CompletionCodeOK, nil
	case CmdGetUserAccess:
		return handleGetUserAccess(msg.Data, state)
	case CmdGetUserName:
		return handleGetUserName(msg.Data, state)
	case CmdSetUserName:
		return handleSetUserName(msg.Data, state)
	case CmdSetUserPassword:
		return handleSetUserPassword(msg.Data, state)
	case CmdSetUserAccess:
		return handleSetUserAccess(msg.Data, state)
	case CmdGetChannelAccess:
		return handleGetChannelAccess(msg.Data, state)
	case CmdSetChannelAccess:
		return handleSetChannelAccess(msg.Data, state)
	case CmdGetChannelInfo:
		return handleGetChannelInfo(msg.Data, state)
	default:
		return CompletionCodeInvalidCommand, nil
	}
}

func handleGetDeviceID() (CompletionCode, []byte) {
	// Static response for a virtual BMC
	data := []byte{
		0x20,                   // Device ID
		0x01,                   // Device Revision
		0x02,                   // Firmware Revision 1
		0x00,                   // Firmware Revision 2
		0x02,                   // IPMI Version (2.0)
		0xBF,                   // Additional Device Support
		0x00, 0x00, 0x00,       // Manufacturer ID (3 bytes)
		0x00, 0x00,             // Product ID (2 bytes)
		0x00, 0x00, 0x00, 0x00, // Aux Firmware Rev
	}
	return CompletionCodeOK, data
}

func handleGetChannelAuthCapabilities(reqData []byte) (CompletionCode, []byte) {
	data := []byte{
		0x01, // Channel number
		0x97, // Auth type support: RMCP+ (0x80) + password (0x10) + MD5 (0x04) + MD2 (0x02) + none (0x01)
		0x06, // Auth status: non-null users + null users
		0x02, // Extended capabilities: Channel 20 (IPMI 2.0)
		0x00, 0x00, 0x00, // OEM ID
		0x00, // OEM Aux
	}
	return CompletionCodeOK, data
}

func handleGetSessionChallenge(reqData []byte) (CompletionCode, []byte) {
	// Generate temporary session ID and challenge
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return CompletionCodeUnspecified, nil
	}
	ipmi15TempSessionID = binary.LittleEndian.Uint32(b)

	if _, err := rand.Read(ipmi15Challenge[:]); err != nil {
		return CompletionCodeUnspecified, nil
	}

	resp := make([]byte, 20)
	binary.LittleEndian.PutUint32(resp[0:4], ipmi15TempSessionID)
	copy(resp[4:20], ipmi15Challenge[:])
	return CompletionCodeOK, resp
}

func handleActivateSession(reqData []byte) (CompletionCode, []byte) {
	if len(reqData) < 22 {
		return CompletionCodeInvalidField, nil
	}

	authType := reqData[0]
	maxPriv := reqData[1]

	// Generate active session ID
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return CompletionCodeUnspecified, nil
	}
	ipmi15ActiveSessionID = binary.LittleEndian.Uint32(b)
	// Avoid zero session ID
	if ipmi15ActiveSessionID == 0 {
		ipmi15ActiveSessionID = 1
	}

	resp := make([]byte, 10)
	resp[0] = authType
	binary.LittleEndian.PutUint32(resp[1:5], ipmi15ActiveSessionID)
	binary.LittleEndian.PutUint32(resp[5:9], 1) // initial inbound seq
	resp[9] = maxPriv
	return CompletionCodeOK, resp
}

func handleSetSessionPrivilege(reqData []byte) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}
	return CompletionCodeOK, []byte{reqData[0]}
}

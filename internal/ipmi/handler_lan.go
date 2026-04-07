package ipmi

import (
	"github.com/steigr/qemu-bmc/internal/bmc"
)

// lanConfigRevision is the parameter revision byte returned in Get LAN Config responses.
// 0x11 = revision 1.1 per IPMI spec.
const lanConfigRevision = 0x11

// supportedLANParams defines which LAN config parameter numbers are recognized.
var supportedLANParams = map[uint8]bool{
	0:  true, // Set In Progress
	1:  true, // Auth Type Support
	2:  true, // Auth Type Enables
	3:  true, // IP Address
	4:  true, // IP Source
	5:  true, // MAC Address
	6:  true, // Subnet Mask
	7:  true, // Default Gateway IP (alias)
	12: true, // Default Gateway
	13: true, // Default Gateway MAC
}

// readOnlyLANParams defines which LAN config parameters cannot be written.
var readOnlyLANParams = map[uint8]bool{
	1: true, // Auth Type Support
}

// handleGetLANConfigParams handles Get LAN Configuration Parameters (cmd 0x02).
// Request (4 bytes): [channel] [param_selector] [set_selector] [block_selector]
// Response: [revision (0x11)] [param_data...]
func handleGetLANConfigParams(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 4 {
		return CompletionCodeInvalidField, nil
	}

	param := reqData[1]

	// Check if parameter is in supported set
	if !supportedLANParams[param] {
		return CompletionCodeParameterOutOfRange, nil
	}

	// Param 0 (Set In Progress) is special: always return "set complete" (0x00)
	if param == 0 {
		return CompletionCodeOK, []byte{lanConfigRevision, 0x00}
	}

	// Look up parameter data from state
	paramData := state.GetLANConfig(param)
	if paramData == nil {
		return CompletionCodeParameterOutOfRange, nil
	}

	// Build response: [revision] [param_data...]
	resp := make([]byte, 1+len(paramData))
	resp[0] = lanConfigRevision
	copy(resp[1:], paramData)

	return CompletionCodeOK, resp
}

// handleSetLANConfigParams handles Set LAN Configuration Parameters (cmd 0x01).
// Request (2+ bytes): [channel] [param_selector] [data...]
// Response: empty on success
func handleSetLANConfigParams(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 2 {
		return CompletionCodeInvalidField, nil
	}

	param := reqData[1]

	// Check if parameter is in supported set
	if !supportedLANParams[param] {
		return CompletionCodeParameterOutOfRange, nil
	}

	// Param 0 (Set In Progress): accept but ignore (no-op)
	if param == 0 {
		return CompletionCodeOK, nil
	}

	// Check read-only params
	if readOnlyLANParams[param] {
		return CompletionCodeInvalidField, nil
	}

	// Store param data (everything after channel and param_selector)
	state.SetLANConfig(param, reqData[2:])

	return CompletionCodeOK, nil
}

// handleTransportCommand dispatches IPMI Transport (NetFn 0x0C) commands.
func handleTransportCommand(msg *IPMIMessage, state *bmc.State) (CompletionCode, []byte) {
	switch msg.Command {
	case CmdGetLANConfigParams:
		return handleGetLANConfigParams(msg.Data, state)
	case CmdSetLANConfigParams:
		return handleSetLANConfigParams(msg.Data, state)
	default:
		return CompletionCodeInvalidCommand, nil
	}
}

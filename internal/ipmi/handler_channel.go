package ipmi

import (
	"github.com/steigr/qemu-bmc/internal/bmc"
)

// handleGetChannelAccess returns channel access data.
// Request (2 bytes):
//
//	Byte 0: channel number (bits 3:0)
//	Byte 1: access type (bits 7:6): 0x40=non-volatile, 0x80=volatile
//
// Response (2 bytes):
//
//	Byte 0: [alerting_disabled(1)][per_msg_auth(1)][user_level_auth(1)][reserved(2)][access_mode(3)]
//	Byte 1: [reserved(4)][privilege_limit(4)]
func handleGetChannelAccess(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 2 {
		return CompletionCodeInvalidField, nil
	}

	channel := reqData[0] & 0x0F
	// reqData[1] bits 7:6 determine volatile vs non-volatile; we return the same for both.

	access := state.GetChannelAccess(channel)

	// Bit layout: [alerting_disabled(7)][per_msg_auth(6)][user_level_auth(5)][reserved(4:3)][access_mode(2:0)]
	var byte0 byte
	byte0 = access.AccessMode & 0x07 // bits 2:0
	if access.UserLevelAuth {
		byte0 |= 0x20 // bit 5
	}
	if access.PerMsgAuth {
		byte0 |= 0x40 // bit 6
	}
	if !access.AlertingEnabled {
		byte0 |= 0x80 // bit 7: alerting_disabled (inverted)
	}

	byte1 := access.PrivilegeLimit & 0x0F

	return CompletionCodeOK, []byte{byte0, byte1}
}

// handleSetChannelAccess sets channel access data.
// Request (3 bytes):
//
//	Byte 0: [access_set_mode(2)][reserved(2)][channel(4)]
//	Byte 1: [alerting_disabled(1)][per_msg_auth(1)][user_level_auth(1)][reserved(2)][access_mode(3)]
//	Byte 2: [priv_set_mode(2)][reserved(2)][privilege_limit(4)]
//
// Response: empty (CompletionCodeOK)
func handleSetChannelAccess(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 3 {
		return CompletionCodeInvalidField, nil
	}

	channel := reqData[0] & 0x0F

	// Parse byte 1: access fields
	// Bit layout: [alerting_disabled(7)][per_msg_auth(6)][user_level_auth(5)][reserved(4:3)][access_mode(2:0)]
	accessMode := reqData[1] & 0x07
	userLevelAuth := reqData[1]&0x20 != 0
	perMsgAuth := reqData[1]&0x40 != 0
	alertingDisabled := reqData[1]&0x80 != 0

	// Parse byte 2: privilege limit
	privLimit := reqData[2] & 0x0F

	access := bmc.ChannelAccess{
		AccessMode:      accessMode,
		UserLevelAuth:   userLevelAuth,
		PerMsgAuth:      perMsgAuth,
		AlertingEnabled: !alertingDisabled,
		PrivilegeLimit:  privLimit,
	}

	state.SetChannelAccess(channel, access)
	return CompletionCodeOK, nil
}

// handleGetChannelInfo returns static channel information.
// Request (1 byte): channel number (bits 3:0). 0x0E = current channel (resolves to 1).
// Response (9 bytes):
//
//	Byte 0: channel number
//	Byte 1: channel medium type
//	Byte 2: channel protocol type
//	Byte 3: [session_support(2)][active_session_count(6)]
//	Bytes 4-6: vendor ID (zeros)
//	Bytes 7-8: aux channel info (zeros)
func handleGetChannelInfo(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}

	channel := reqData[0] & 0x0F
	if channel == 0x0E {
		channel = 1 // current channel resolves to channel 1
	}

	info := state.GetChannelInfo(channel)

	byte3 := ((info.SessionSupport & 0x03) << 6) | (info.ActiveSessions & 0x3F)

	data := []byte{
		info.ChannelNumber,
		info.ChannelMedium,
		info.ChannelProtocol,
		byte3,
		0x00, 0x00, 0x00, // vendor ID
		0x00, 0x00, // aux channel info
	}
	return CompletionCodeOK, data
}

package ipmi

import (
	"strings"

	"github.com/steigr/qemu-bmc/internal/bmc"
)

// handleGetUserAccess returns user access data for a given channel and user ID.
// Request: [channel(1 byte, bits 3:0)] [user_id(1 byte, bits 5:0)]
// Response (4 bytes):
//
//	Byte 0: maxUsers (bits 5:0)
//	Byte 1: enabledCount (bits 5:0)
//	Byte 2: fixed-name user count
//	Byte 3: privilege + flags
func handleGetUserAccess(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 2 {
		return CompletionCodeInvalidField, nil
	}

	channel := reqData[0] & 0x0F
	userID := reqData[1] & 0x3F

	access, err := state.GetUserAccess(channel, userID)
	if err != nil {
		return CompletionCodeParameterOutOfRange, nil
	}

	maxUsers := state.MaxUsers()
	enabledCount := state.EnabledUserCount()

	var flagsByte byte
	flagsByte = access.PrivilegeLimit & 0x0F
	if access.IPMIMessaging {
		flagsByte |= 0x10
	}
	if access.LinkAuth {
		flagsByte |= 0x20
	}
	if access.CallinCallback {
		flagsByte |= 0x40
	}

	data := []byte{
		maxUsers & 0x3F,
		enabledCount & 0x3F,
		0x01, // 1 fixed-name user (User1)
		flagsByte,
	}
	return CompletionCodeOK, data
}

// handleGetUserName returns the name for a given user slot as a 16-byte zero-padded field.
// Request: [user_id(1 byte, bits 5:0)]
// Response: 16-byte name (zero-padded)
func handleGetUserName(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}

	userID := reqData[0] & 0x3F

	name, err := state.GetUserName(userID)
	if err != nil {
		return CompletionCodeParameterOutOfRange, nil
	}

	data := make([]byte, 16)
	copy(data, name)
	return CompletionCodeOK, data
}

// handleSetUserName sets the name for a given user slot.
// Request: [user_id(1 byte)] [name(16 bytes)] — 17 bytes total
// Response: empty (CompletionCodeOK)
func handleSetUserName(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 17 {
		return CompletionCodeInvalidField, nil
	}

	userID := reqData[0] & 0x3F
	nameBytes := reqData[1:17]

	// Trim null bytes to get the actual name string
	name := strings.TrimRight(string(nameBytes), "\x00")

	if err := state.SetUserName(userID, name); err != nil {
		return CompletionCodeParameterOutOfRange, nil
	}
	return CompletionCodeOK, nil
}

// handleSetUserPassword handles set/test password and enable/disable user operations.
// Request:
//
//	Byte 0: [20-byte flag (bit 7)] [reserved (bit 6)] [user_id (bits 5:0)]
//	Byte 1: [reserved (bits 7:2)] [operation (bits 1:0)]
//	    0=disable user, 1=enable user, 2=set password, 3=test password
//	Bytes 2+: password (16 or 20 bytes, null-terminated)
func handleSetUserPassword(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 2 {
		return CompletionCodeInvalidField, nil
	}

	userID := reqData[0] & 0x3F
	is20Byte := reqData[0]&0x80 != 0
	operation := reqData[1] & 0x03

	passLen := 16
	if is20Byte {
		passLen = 20
	}

	switch operation {
	case 0x00: // disable user
		access, err := state.GetUserAccess(0, userID)
		if err != nil {
			return CompletionCodeParameterOutOfRange, nil
		}
		access.Enabled = false
		if err := state.SetUserAccess(0, userID, access); err != nil {
			return CompletionCodeParameterOutOfRange, nil
		}
		return CompletionCodeOK, nil

	case 0x01: // enable user
		access, err := state.GetUserAccess(0, userID)
		if err != nil {
			return CompletionCodeParameterOutOfRange, nil
		}
		access.Enabled = true
		if err := state.SetUserAccess(0, userID, access); err != nil {
			return CompletionCodeParameterOutOfRange, nil
		}
		return CompletionCodeOK, nil

	case 0x02: // set password
		if len(reqData) < 2+passLen {
			return CompletionCodeInvalidField, nil
		}
		passBytes := reqData[2 : 2+passLen]
		password := strings.TrimRight(string(passBytes), "\x00")
		if err := state.SetUserPassword(userID, password); err != nil {
			return CompletionCodeParameterOutOfRange, nil
		}
		return CompletionCodeOK, nil

	case 0x03: // test password
		if len(reqData) < 2+passLen {
			return CompletionCodeInvalidField, nil
		}
		passBytes := reqData[2 : 2+passLen]
		password := strings.TrimRight(string(passBytes), "\x00")
		if state.CheckPassword(userID, password) {
			return CompletionCodeOK, nil
		}
		return CompletionCodeInvalidField, nil

	default:
		return CompletionCodeInvalidField, nil
	}
}

// handleSetUserAccess sets access settings for a user on a given channel.
// Request (4 bytes):
//
//	Byte 0: [change_enable(bit 7)] [callin(bit 6)] [link_auth(bit 5)] [ipmi_msg(bit 4)] [channel(bits 3:0)]
//	Byte 1: [reserved(bits 7:6)] [user_id(bits 5:0)]
//	Byte 2: [reserved(bits 7:4)] [privilege_limit(bits 3:0)]
//	Byte 3: [reserved(bits 7:4)] [session_limit(bits 3:0)] (ignored)
func handleSetUserAccess(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 4 {
		return CompletionCodeInvalidField, nil
	}

	channel := reqData[0] & 0x0F
	ipmiMsg := reqData[0]&0x10 != 0
	linkAuth := reqData[0]&0x20 != 0
	callin := reqData[0]&0x40 != 0

	userID := reqData[1] & 0x3F
	privLimit := reqData[2] & 0x0F

	// Preserve existing Enabled state (enable/disable is done via Set User Password)
	existing, _ := state.GetUserAccess(channel, userID)

	access := bmc.UserAccess{
		PrivilegeLimit: privLimit,
		Enabled:        existing.Enabled,
		IPMIMessaging:  ipmiMsg,
		LinkAuth:       linkAuth,
		CallinCallback: callin,
	}

	if err := state.SetUserAccess(channel, userID, access); err != nil {
		return CompletionCodeParameterOutOfRange, nil
	}
	return CompletionCodeOK, nil
}

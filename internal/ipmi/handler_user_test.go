package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/bmc"
)

func newTestBMCState() *bmc.State {
	return bmc.NewState("admin", "password")
}

func TestHandleGetUserAccess(t *testing.T) {
	state := newTestBMCState()
	// Channel 1, user 2 (default admin)
	reqData := []byte{0x01, 0x02}
	code, data := handleGetUserAccess(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 4)
	assert.Equal(t, byte(15), data[0]&0x3F) // maxUsers=15
	assert.Equal(t, byte(1), data[1]&0x3F)  // enabledCount=1
	assert.Equal(t, byte(0x01), data[2])     // 1 fixed-name user
	assert.Equal(t, byte(4), data[3]&0x0F)   // privLimit=4 (Admin)
	assert.NotZero(t, data[3]&0x10)          // IPMIMessaging=true
	assert.NotZero(t, data[3]&0x20)          // LinkAuth=true
}

func TestHandleGetUserAccess_InvalidData(t *testing.T) {
	state := newTestBMCState()
	code, _ := handleGetUserAccess([]byte{}, state)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestHandleGetUserName(t *testing.T) {
	state := newTestBMCState()
	// User 2 is "admin"
	reqData := []byte{0x02}
	code, data := handleGetUserName(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 16)
	assert.Equal(t, "admin", string(data[:5]))
	// Remaining bytes should be zero
	for i := 5; i < 16; i++ {
		assert.Equal(t, byte(0), data[i], "byte %d should be zero", i)
	}
}

func TestHandleGetUserName_EmptySlot(t *testing.T) {
	state := newTestBMCState()
	// User 5 has no name set
	reqData := []byte{0x05}
	code, data := handleGetUserName(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 16)
	// All bytes should be zero
	for i := 0; i < 16; i++ {
		assert.Equal(t, byte(0), data[i], "byte %d should be zero", i)
	}
}

func TestHandleSetUserName(t *testing.T) {
	state := newTestBMCState()
	// Set user 3 to "maas"
	reqData := make([]byte, 17)
	reqData[0] = 0x03
	copy(reqData[1:], "maas")
	code, data := handleSetUserName(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Nil(t, data)
	// Verify via state
	name, err := state.GetUserName(3)
	require.NoError(t, err)
	assert.Equal(t, "maas", name)
}

func TestHandleSetUserPassword(t *testing.T) {
	state := newTestBMCState()
	// Operation 0x02 (set password) for user 3
	// Byte 0: [20byte(bit7)][reserved(bit6)][user_id(bits5:0)]
	// Byte 1: [reserved(bits7:2)][operation(bits1:0)]
	// Bytes 2+: password (16 bytes, null-padded)
	reqData := make([]byte, 18) // 2 header bytes + 16 password bytes
	reqData[0] = 0x03           // user_id=3, 16-byte password (bit7=0)
	reqData[1] = 0x02           // operation=set password
	copy(reqData[2:], "newpass")
	code, data := handleSetUserPassword(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Nil(t, data)
	// Verify password was set
	assert.True(t, state.CheckPassword(3, "newpass"))
}

func TestHandleSetUserPassword_TestPassword(t *testing.T) {
	state := newTestBMCState()
	// Test correct password for user 2 (admin/password)
	reqData := make([]byte, 18)
	reqData[0] = 0x02 // user_id=2
	reqData[1] = 0x03 // operation=test password
	copy(reqData[2:], "password")
	code, _ := handleSetUserPassword(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)

	// Test wrong password
	reqData2 := make([]byte, 18)
	reqData2[0] = 0x02 // user_id=2
	reqData2[1] = 0x03 // operation=test password
	copy(reqData2[2:], "wrongpass")
	code2, _ := handleSetUserPassword(reqData2, state)
	assert.NotEqual(t, CompletionCodeOK, code2)
}

func TestHandleSetUserAccess(t *testing.T) {
	state := newTestBMCState()

	// Enable user 3 first (as MaaS does via Set User Password, operation=enable)
	enableReq := make([]byte, 2)
	enableReq[0] = 0x03 // user_id=3
	enableReq[1] = 0x01 // operation=enable
	handleSetUserPassword(enableReq, state)

	// Set user 3 with admin privilege on channel 1
	reqData := []byte{
		0x91, // change_enable(1) | callin(0) | link_auth(0) | ipmi_msg(1) | channel(1)
		0x03, // user_id=3
		0x04, // privilege_limit=4 (Admin)
		0x00, // session_limit (ignored)
	}
	code, data := handleSetUserAccess(reqData, state)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Nil(t, data)
	// Verify via state
	access, err := state.GetUserAccess(1, 3)
	require.NoError(t, err)
	assert.Equal(t, uint8(4), access.PrivilegeLimit)
	assert.True(t, access.IPMIMessaging)
	assert.True(t, access.Enabled)
	assert.False(t, access.LinkAuth)
	assert.False(t, access.CallinCallback)
}

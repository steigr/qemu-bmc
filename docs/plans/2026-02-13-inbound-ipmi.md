# Inbound IPMI (VM Protocol) Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Enable QEMU VMs to communicate with qemu-bmc via the in-band KCS/IPMI interface, supporting MaaS commissioning scripts (`bmc_config.py`) that configure BMC users, LAN settings, and channel access from within the guest OS.

**Architecture:** QEMU's `ipmi-bmc-extern` device connects to qemu-bmc via a TCP chardev socket using the OpenIPMI VM wire protocol. qemu-bmc acts as an external BMC emulator, handling IPMI commands from the guest (user management, LAN config, channel access) and reusing existing command handlers (chassis, device ID). A new `internal/bmc/` package stores BMC configuration state (user accounts, LAN parameters, channel settings) shared across all IPMI transports.

**Tech Stack:** Go stdlib (`net`, `sync`, `encoding/binary`), existing `internal/ipmi/` command handler pattern, `testify/assert`+`testify/require` for testing.

---

## Background: MaaS Commissioning Flow

During commissioning, MaaS boots an ephemeral Ubuntu image inside the VM. The commissioning script `bmc_config.py` runs **inside the guest** and uses FreeIPMI tools (`bmc-config`, `ipmi-locate`, `ipmitool`) to:

1. **Detect** the BMC via KCS interface (`/dev/ipmi0`)
2. **Read** existing BMC configuration (users, LAN, channel access)
3. **Create** a MAAS-specific IPMI user with admin privileges
4. **Configure** LAN access (auth types, access modes)
5. **Report** BMC IP + credentials back to MaaS controller

After commissioning, MaaS uses these credentials for out-of-band power management (IPMI over LAN / Redfish).

### QEMU Configuration

```bash
qemu-system-x86_64 \
  -chardev socket,id=ipmi0,host=localhost,port=9002,reconnect=10 \
  -device ipmi-bmc-extern,id=bmc0,chardev=ipmi0 \
  -device isa-ipmi-kcs,bmc=bmc0
```

### OpenIPMI VM Wire Protocol

The chardev uses a byte-oriented protocol with:
- **IPMI messages** terminated by `0xA0` (VM_MSG_CHAR)
- **Hardware control commands** terminated by `0xA1` (VM_CMD_CHAR)
- **Escape byte** `0xAA` (VM_ESCAPE_CHAR): clears bit 4 of next byte
- **Checksum**: two's complement (same as existing IPMI)

**IPMI message format (host → BMC):**
```
[seq] [netfn<<2 | lun] [cmd] [data...] [checksum] 0xA0
```

**IPMI response format (BMC → host):**
```
[seq] [netfn<<2 | lun] [cmd] [completion_code] [data...] [checksum] 0xA0
```

**Connection handshake (QEMU → BMC):**
```
[0xFF] [version=1] 0xA1       # Version command
[0x08] [capabilities] 0xA1    # Capabilities command
```

**Hardware control commands (BMC → QEMU):**

| Code | Name | Description |
|------|------|-------------|
| 0x00 | NOATTN | Clear attention |
| 0x01 | ATTN | Set attention |
| 0x02 | ATTN_IRQ | Set attention + IRQ |
| 0x03 | POWEROFF | Power off VM |
| 0x04 | RESET | Reset VM |
| 0x05 | ENABLE_IRQ | Enable IRQ |
| 0x06 | DISABLE_IRQ | Disable IRQ |
| 0x07 | SEND_NMI | Send NMI |
| 0x09 | GRACEFUL_SHUTDOWN | ACPI shutdown |

### Required IPMI Commands for MaaS

| Command | NetFn | Cmd | Status |
|---------|-------|-----|--------|
| Get Device ID | App (0x06) | 0x01 | **Existing** |
| Get Channel Auth Capabilities | App (0x06) | 0x38 | **Existing** |
| Get Chassis Status | Chassis (0x00) | 0x01 | **Existing** |
| Chassis Control | Chassis (0x00) | 0x02 | **Existing** |
| Set Boot Options | Chassis (0x00) | 0x08 | **Existing** |
| Get Boot Options | Chassis (0x00) | 0x09 | **Existing** |
| Get User Access | App (0x06) | 0x44 | **New** |
| Get User Name | App (0x06) | 0x46 | **New** |
| Set User Name | App (0x06) | 0x45 | **New** |
| Set User Password | App (0x06) | 0x47 | **New** |
| Set User Access | App (0x06) | 0x43 | **New** |
| Get Channel Access | App (0x06) | 0x41 | **New** |
| Set Channel Access | App (0x06) | 0x40 | **New** |
| Get Channel Info | App (0x06) | 0x42 | **New** |
| Get LAN Config Parameters | Transport (0x0C) | 0x02 | **New** |
| Set LAN Config Parameters | Transport (0x0C) | 0x01 | **New** |

---

## Task 1: BMC State — User Account Management

**Files:**
- Create: `internal/bmc/state.go`
- Create: `internal/bmc/state_test.go`

This task creates the `bmc` package to manage BMC configuration state. We start with user accounts only (LAN and channel config added in later tasks).

### Step 1: Write failing tests for user account operations

```go
// internal/bmc/state_test.go
package bmc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewState_InitializesDefaultUser(t *testing.T) {
	s := NewState("admin", "password")

	// User1 is reserved (null user per IPMI spec)
	name, err := s.GetUserName(1)
	require.NoError(t, err)
	assert.Equal(t, "", name)

	// User2 is the default admin
	name, err = s.GetUserName(2)
	require.NoError(t, err)
	assert.Equal(t, "admin", name)

	access, err := s.GetUserAccess(1, 2) // channel 1, user 2
	require.NoError(t, err)
	assert.True(t, access.Enabled)
	assert.True(t, access.IPMIMessaging)
	assert.True(t, access.LinkAuth)
	assert.Equal(t, uint8(4), access.PrivilegeLimit) // Admin
}

func TestSetGetUserName(t *testing.T) {
	s := NewState("admin", "password")

	// Set user 3
	err := s.SetUserName(3, "maas")
	require.NoError(t, err)

	name, err := s.GetUserName(3)
	require.NoError(t, err)
	assert.Equal(t, "maas", name)
}

func TestSetUserName_InvalidSlot(t *testing.T) {
	s := NewState("admin", "password")

	err := s.SetUserName(0, "test")
	assert.Error(t, err)

	err = s.SetUserName(16, "test")
	assert.Error(t, err)
}

func TestSetUserPassword(t *testing.T) {
	s := NewState("admin", "password")

	err := s.SetUserPassword(3, "secret123")
	require.NoError(t, err)

	assert.True(t, s.CheckPassword(3, "secret123"))
	assert.False(t, s.CheckPassword(3, "wrong"))
}

func TestSetGetUserAccess(t *testing.T) {
	s := NewState("admin", "password")

	access := UserAccess{
		PrivilegeLimit: 4,
		Enabled:        true,
		IPMIMessaging:  true,
		LinkAuth:       true,
		CallinCallback: false,
	}
	err := s.SetUserAccess(1, 3, access)
	require.NoError(t, err)

	got, err := s.GetUserAccess(1, 3)
	require.NoError(t, err)
	assert.Equal(t, access, got)
}

func TestGetMaxUsers(t *testing.T) {
	s := NewState("admin", "password")
	assert.Equal(t, uint8(15), s.MaxUsers())
}

func TestGetEnabledUserCount(t *testing.T) {
	s := NewState("admin", "password")

	// Default: only User2 is enabled
	assert.Equal(t, uint8(1), s.EnabledUserCount())

	// Enable User3
	s.SetUserName(3, "maas")
	s.SetUserAccess(1, 3, UserAccess{Enabled: true, PrivilegeLimit: 4, IPMIMessaging: true, LinkAuth: true})
	assert.Equal(t, uint8(2), s.EnabledUserCount())
}

func TestLookupUserByName(t *testing.T) {
	s := NewState("admin", "password")

	// Lookup default admin
	userID, found := s.LookupUserByName("admin")
	assert.True(t, found)
	assert.Equal(t, uint8(2), userID)

	// Lookup non-existent user
	_, found = s.LookupUserByName("nobody")
	assert.False(t, found)

	// Add and lookup
	s.SetUserName(5, "maas")
	userID, found = s.LookupUserByName("maas")
	assert.True(t, found)
	assert.Equal(t, uint8(5), userID)
}
```

### Step 2: Run tests to verify RED

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/bmc/... -v -count=1`
Expected: FAIL — package does not exist

### Step 3: Implement BMC state

```go
// internal/bmc/state.go
package bmc

import (
	"fmt"
	"sync"
)

const maxUsers = 15

// UserAccess represents user access settings for a channel
type UserAccess struct {
	PrivilegeLimit uint8 // 1=Callback, 2=User, 3=Operator, 4=Admin
	Enabled        bool
	IPMIMessaging  bool
	LinkAuth       bool
	CallinCallback bool
}

type userSlot struct {
	name     string
	password string
	access   UserAccess
}

// State holds the BMC's mutable configuration
type State struct {
	users [maxUsers + 1]userSlot // index 0 unused, 1-15 valid
	mu    sync.RWMutex
}

// NewState creates a BMC state with the default admin user in slot 2
func NewState(defaultUser, defaultPass string) *State {
	s := &State{}
	// User1 is null user (reserved per IPMI spec)
	// User2 is the default admin
	s.users[2] = userSlot{
		name:     defaultUser,
		password: defaultPass,
		access: UserAccess{
			PrivilegeLimit: 4, // Admin
			Enabled:        true,
			IPMIMessaging:  true,
			LinkAuth:       true,
		},
	}
	return s
}

// MaxUsers returns the maximum number of user slots
func (s *State) MaxUsers() uint8 {
	return maxUsers
}

// GetUserName returns the username for the given slot (1-15)
func (s *State) GetUserName(userID uint8) (string, error) {
	if userID < 1 || userID > maxUsers {
		return "", fmt.Errorf("invalid user ID: %d", userID)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.users[userID].name, nil
}

// SetUserName sets the username for the given slot (1-15)
func (s *State) SetUserName(userID uint8, name string) error {
	if userID < 1 || userID > maxUsers {
		return fmt.Errorf("invalid user ID: %d", userID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[userID].name = name
	return nil
}

// SetUserPassword sets the password for the given slot
func (s *State) SetUserPassword(userID uint8, password string) error {
	if userID < 1 || userID > maxUsers {
		return fmt.Errorf("invalid user ID: %d", userID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[userID].password = password
	return nil
}

// CheckPassword verifies a password for a user slot
func (s *State) CheckPassword(userID uint8, password string) bool {
	if userID < 1 || userID > maxUsers {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.users[userID].password == password
}

// GetUserAccess returns user access settings for a channel+user
func (s *State) GetUserAccess(channel, userID uint8) (UserAccess, error) {
	if userID < 1 || userID > maxUsers {
		return UserAccess{}, fmt.Errorf("invalid user ID: %d", userID)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.users[userID].access, nil
}

// SetUserAccess sets user access settings for a channel+user
func (s *State) SetUserAccess(channel, userID uint8, access UserAccess) error {
	if userID < 1 || userID > maxUsers {
		return fmt.Errorf("invalid user ID: %d", userID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[userID].access = access
	return nil
}

// EnabledUserCount returns the number of enabled users
func (s *State) EnabledUserCount() uint8 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var count uint8
	for i := 1; i <= maxUsers; i++ {
		if s.users[i].access.Enabled {
			count++
		}
	}
	return count
}

// LookupUserByName finds a user ID by username
func (s *State) LookupUserByName(name string) (uint8, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := uint8(1); i <= maxUsers; i++ {
		if s.users[i].name == name {
			return i, true
		}
	}
	return 0, false
}
```

### Step 4: Run tests to verify GREEN

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/bmc/... -v -count=1`
Expected: All PASS

### Step 5: Commit

```bash
git add internal/bmc/state.go internal/bmc/state_test.go
git commit -m "feat: add BMC state package for user account management"
```

---

## Task 2: BMC State — LAN Configuration & Channel Access

**Files:**
- Modify: `internal/bmc/state.go`
- Modify: `internal/bmc/state_test.go`

Extend BMCState with LAN configuration parameters and channel access settings.

### Step 1: Write failing tests

```go
// Append to internal/bmc/state_test.go

func TestLANConfig_Defaults(t *testing.T) {
	s := NewState("admin", "password")

	ip := s.GetLANConfig(3) // IP Address
	assert.Equal(t, []byte{0, 0, 0, 0}, ip)

	authSupport := s.GetLANConfig(1) // Auth Type Support (read-only)
	assert.Equal(t, []byte{0x97}, authSupport) // RMCP+ | Password | MD5 | MD2 | None
}

func TestLANConfig_SetGet(t *testing.T) {
	s := NewState("admin", "password")

	// Set IP address (param 3)
	s.SetLANConfig(3, []byte{192, 168, 1, 100})
	ip := s.GetLANConfig(3)
	assert.Equal(t, []byte{192, 168, 1, 100}, ip)

	// Set subnet mask (param 6)
	s.SetLANConfig(6, []byte{255, 255, 255, 0})
	mask := s.GetLANConfig(6)
	assert.Equal(t, []byte{255, 255, 255, 0}, mask)

	// Set default gateway (param 12)
	s.SetLANConfig(12, []byte{192, 168, 1, 1})
	gw := s.GetLANConfig(12)
	assert.Equal(t, []byte{192, 168, 1, 1}, gw)
}

func TestLANConfig_IPSource(t *testing.T) {
	s := NewState("admin", "password")

	// Default is static (1)
	src := s.GetLANConfig(4)
	assert.Equal(t, []byte{0x01}, src)

	// Set to DHCP
	s.SetLANConfig(4, []byte{0x02})
	src = s.GetLANConfig(4)
	assert.Equal(t, []byte{0x02}, src)
}

func TestLANConfig_MACAddress(t *testing.T) {
	s := NewState("admin", "password")
	s.SetLANConfig(5, []byte{0x52, 0x54, 0x00, 0x12, 0x34, 0x56})
	mac := s.GetLANConfig(5)
	assert.Equal(t, []byte{0x52, 0x54, 0x00, 0x12, 0x34, 0x56}, mac)
}

func TestLANConfig_AuthTypeEnables(t *testing.T) {
	s := NewState("admin", "password")

	// Param 2: auth enables (5 bytes: callback, user, operator, admin, oem)
	enables := s.GetLANConfig(2)
	assert.Len(t, enables, 5)

	// Set auth enables
	s.SetLANConfig(2, []byte{0x00, 0x14, 0x14, 0x14, 0x00})
	enables = s.GetLANConfig(2)
	assert.Equal(t, []byte{0x00, 0x14, 0x14, 0x14, 0x00}, enables)
}

func TestChannelAccess_Defaults(t *testing.T) {
	s := NewState("admin", "password")
	access := s.GetChannelAccess(1)
	assert.Equal(t, uint8(2), access.AccessMode) // AlwaysAvailable
	assert.Equal(t, uint8(4), access.PrivilegeLimit) // Admin
}

func TestChannelAccess_SetGet(t *testing.T) {
	s := NewState("admin", "password")
	ca := ChannelAccess{
		AccessMode:      2,
		UserLevelAuth:   true,
		PerMsgAuth:      true,
		AlertingEnabled: false,
		PrivilegeLimit:  4,
	}
	s.SetChannelAccess(1, ca)
	got := s.GetChannelAccess(1)
	assert.Equal(t, ca, got)
}

func TestGetChannelInfo(t *testing.T) {
	s := NewState("admin", "password")
	info := s.GetChannelInfo(1)
	assert.Equal(t, uint8(1), info.ChannelNumber)
	assert.Equal(t, uint8(0x04), info.ChannelMedium) // 802.3 LAN
	assert.Equal(t, uint8(0x01), info.ChannelProtocol) // IPMB-1.0
}
```

### Step 2: Run tests to verify RED

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/bmc/... -v -count=1`
Expected: FAIL — undefined: ChannelAccess, GetLANConfig, etc.

### Step 3: Add LAN config and channel access to state.go

Add the following to `internal/bmc/state.go`:

```go
// ChannelAccess represents channel access settings
type ChannelAccess struct {
	AccessMode      uint8 // 0=Disabled, 1=PreBoot, 2=AlwaysAvailable, 3=Shared
	UserLevelAuth   bool
	PerMsgAuth      bool
	AlertingEnabled bool
	PrivilegeLimit  uint8 // Max privilege for channel
}

// ChannelInfo holds static channel information
type ChannelInfo struct {
	ChannelNumber   uint8
	ChannelMedium   uint8 // 0x04 = 802.3 LAN
	ChannelProtocol uint8 // 0x01 = IPMB-1.0
	SessionSupport  uint8 // 0x02 = multi-session
	ActiveSessions  uint8
}
```

Extend the `State` struct:

```go
type State struct {
	users      [maxUsers + 1]userSlot
	lanConfig  map[uint8][]byte    // param number -> value
	channelAccess [16]ChannelAccess // indexed by channel number (0-15)
	mu         sync.RWMutex
}
```

Update `NewState` to initialize LAN defaults:

```go
func NewState(defaultUser, defaultPass string) *State {
	s := &State{
		lanConfig: make(map[uint8][]byte),
	}
	// ... user init ...

	// LAN config defaults
	s.lanConfig[1] = []byte{0x97}                         // Auth Type Support (read-only)
	s.lanConfig[2] = []byte{0x14, 0x14, 0x14, 0x14, 0x00} // Auth Type Enables (RMCP+ + Password for each priv)
	s.lanConfig[3] = []byte{0, 0, 0, 0}                   // IP Address
	s.lanConfig[4] = []byte{0x01}                          // IP Source: Static
	s.lanConfig[5] = []byte{0, 0, 0, 0, 0, 0}             // MAC Address
	s.lanConfig[6] = []byte{0, 0, 0, 0}                   // Subnet Mask
	s.lanConfig[12] = []byte{0, 0, 0, 0}                  // Default Gateway

	// Channel 1 defaults (LAN)
	s.channelAccess[1] = ChannelAccess{
		AccessMode:     2, // AlwaysAvailable
		UserLevelAuth:  true,
		PerMsgAuth:     true,
		PrivilegeLimit: 4, // Admin
	}
	return s
}
```

Add methods:

```go
func (s *State) GetLANConfig(param uint8) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.lanConfig[param]; ok {
		result := make([]byte, len(v))
		copy(result, v)
		return result
	}
	return nil
}

func (s *State) SetLANConfig(param uint8, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := make([]byte, len(data))
	copy(value, data)
	s.lanConfig[param] = value
}

func (s *State) GetChannelAccess(channel uint8) ChannelAccess {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if channel > 15 {
		return ChannelAccess{}
	}
	return s.channelAccess[channel]
}

func (s *State) SetChannelAccess(channel uint8, access ChannelAccess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channel <= 15 {
		s.channelAccess[channel] = access
	}
}

func (s *State) GetChannelInfo(channel uint8) ChannelInfo {
	return ChannelInfo{
		ChannelNumber:   channel,
		ChannelMedium:   0x04, // 802.3 LAN
		ChannelProtocol: 0x01, // IPMB-1.0
		SessionSupport:  0x02, // Multi-session
	}
}
```

### Step 4: Run tests to verify GREEN

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/bmc/... -v -count=1`
Expected: All PASS

### Step 5: Commit

```bash
git add internal/bmc/state.go internal/bmc/state_test.go
git commit -m "feat: add LAN configuration and channel access to BMC state"
```

---

## Task 3: IPMI User Management Command Handlers

**Files:**
- Create: `internal/ipmi/handler_user.go`
- Create: `internal/ipmi/handler_user_test.go`
- Modify: `internal/ipmi/types.go` (add constants)
- Modify: `internal/ipmi/handler_app.go` (add routing)

### Step 1: Add constants to types.go

Add to the "IPMI App Commands" block in `internal/ipmi/types.go`:

```go
// IPMI App Commands - User Management
const (
	CmdSetChannelAccess = 0x40
	CmdGetChannelAccess = 0x41
	CmdGetChannelInfo   = 0x42
	CmdSetUserAccess    = 0x43
	CmdGetUserAccess    = 0x44
	CmdSetUserName      = 0x45
	CmdGetUserName      = 0x46
	CmdSetUserPassword  = 0x47
)
```

### Step 2: Write failing tests for user management handlers

```go
// internal/ipmi/handler_user_test.go
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
	s := newTestBMCState()

	// Request: [channel(4bit)|user_to_query(6bit)]
	// Channel 1, User 2 (default admin)
	reqData := []byte{0x12} // channel=1, user_id=2
	code, data := handleGetUserAccess(reqData, s)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 4)

	maxUsers := data[0] & 0x3F
	assert.Equal(t, uint8(15), maxUsers)

	enabledCount := data[1] & 0x3F
	assert.Equal(t, uint8(1), enabledCount)

	// data[3]: user privilege & flags
	privLimit := data[3] & 0x0F
	assert.Equal(t, uint8(4), privLimit) // Admin
}

func TestHandleGetUserAccess_InvalidData(t *testing.T) {
	s := newTestBMCState()
	code, _ := handleGetUserAccess([]byte{}, s)
	assert.Equal(t, CompletionCodeInvalidField, code)
}

func TestHandleGetUserName(t *testing.T) {
	s := newTestBMCState()

	// Request: [user_id]
	code, data := handleGetUserName([]byte{0x02}, s) // User 2
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 16) // fixed 16-byte name field
	assert.Equal(t, "admin", string(data[:5]))
}

func TestHandleGetUserName_EmptySlot(t *testing.T) {
	s := newTestBMCState()
	code, data := handleGetUserName([]byte{0x05}, s)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 16)
	assert.Equal(t, make([]byte, 16), data)
}

func TestHandleSetUserName(t *testing.T) {
	s := newTestBMCState()

	// Request: [user_id] [name(16 bytes)]
	req := make([]byte, 17)
	req[0] = 0x03 // User 3
	copy(req[1:], []byte("maas"))
	code, _ := handleSetUserName(req, s)
	assert.Equal(t, CompletionCodeOK, code)

	// Verify
	name, _ := s.GetUserName(3)
	assert.Equal(t, "maas", name)
}

func TestHandleSetUserPassword(t *testing.T) {
	s := newTestBMCState()

	// Request: [user_id(6bit)|operation(2bit)] [password(16 or 20 bytes)]
	req := make([]byte, 18)
	req[0] = 0x03                    // User 3, operation=0 (set password, 16-byte)
	copy(req[1:], []byte("mysecret"))
	// Remaining bytes are zero-padded

	// First set a username
	s.SetUserName(3, "maas")

	code, _ := handleSetUserPassword(req, s)
	assert.Equal(t, CompletionCodeOK, code)
	assert.True(t, s.CheckPassword(3, "mysecret"))
}

func TestHandleSetUserPassword_TestPassword(t *testing.T) {
	s := newTestBMCState()

	// Operation 0x03 = test password (16-byte)
	req := make([]byte, 18)
	req[0] = 0x02 | 0x03<<4 // User 2, operation=test (bits 5:4 = 11)
	copy(req[1:], []byte("password"))

	code, _ := handleSetUserPassword(req, s)
	assert.Equal(t, CompletionCodeOK, code)
}

func TestHandleSetUserAccess(t *testing.T) {
	s := newTestBMCState()

	// Request: [channel(4bit)|flags(4bit)] [user_id(6bit)|priv_limit(4bit)] ...
	req := []byte{
		0x91,       // channel=1, callin=0, link=0, ipmi_msg=1, change_bits=1 (bit7)
		0x34,       // user_id=3, privilege=4 (admin)
		0x00, 0x00, // session limit, (padding)
	}

	s.SetUserName(3, "maas")
	code, _ := handleSetUserAccess(req, s)
	assert.Equal(t, CompletionCodeOK, code)

	access, _ := s.GetUserAccess(1, 3)
	assert.Equal(t, uint8(4), access.PrivilegeLimit)
	assert.True(t, access.IPMIMessaging)
}
```

### Step 3: Run tests to verify RED

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestHandle.*User -count=1`
Expected: FAIL — undefined functions

### Step 4: Implement user management handlers

```go
// internal/ipmi/handler_user.go
package ipmi

import (
	"strings"

	"github.com/steigr/qemu-bmc/internal/bmc"
)

func handleGetUserAccess(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}

	userID := reqData[0] & 0x3F

	access, err := state.GetUserAccess(1, userID)
	if err != nil {
		return CompletionCodeParameterOutOfRange, nil
	}

	maxUsers := state.MaxUsers()
	enabledCount := state.EnabledUserCount()

	// Byte 0: max user IDs (6 bits)
	// Byte 1: count of enabled users (6 bits)
	// Byte 2: count of fixed-name users (6 bits) — User1 is fixed
	// Byte 3: privilege limit (4 bits) | flags
	data := make([]byte, 4)
	data[0] = maxUsers & 0x3F
	data[1] = enabledCount & 0x3F
	data[2] = 0x01 // 1 fixed-name user (User1)

	privByte := access.PrivilegeLimit & 0x0F
	if access.IPMIMessaging {
		privByte |= 0x10
	}
	if access.LinkAuth {
		privByte |= 0x20
	}
	if access.CallinCallback {
		privByte |= 0x40
	}
	data[3] = privByte

	return CompletionCodeOK, data
}

func handleGetUserName(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}

	userID := reqData[0] & 0x3F
	name, err := state.GetUserName(userID)
	if err != nil {
		return CompletionCodeParameterOutOfRange, nil
	}

	// Return 16-byte fixed-length name
	data := make([]byte, 16)
	copy(data, []byte(name))
	return CompletionCodeOK, data
}

func handleSetUserName(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 17 {
		return CompletionCodeInvalidField, nil
	}

	userID := reqData[0] & 0x3F
	name := strings.TrimRight(string(reqData[1:17]), "\x00")

	if err := state.SetUserName(userID, name); err != nil {
		return CompletionCodeParameterOutOfRange, nil
	}
	return CompletionCodeOK, nil
}

func handleSetUserPassword(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 2 {
		return CompletionCodeInvalidField, nil
	}

	userID := reqData[0] & 0x3F
	operation := (reqData[0] >> 4) & 0x03

	// Determine password length: 16 or 20 bytes
	passLen := 16
	if reqData[0]&0x80 != 0 {
		passLen = 20
	}
	if len(reqData) < 1+passLen {
		return CompletionCodeInvalidField, nil
	}

	password := strings.TrimRight(string(reqData[1:1+passLen]), "\x00")

	switch operation {
	case 0x00: // Disable user
		access, _ := state.GetUserAccess(1, userID)
		access.Enabled = false
		state.SetUserAccess(1, userID, access)
		return CompletionCodeOK, nil
	case 0x01: // Enable user
		access, _ := state.GetUserAccess(1, userID)
		access.Enabled = true
		state.SetUserAccess(1, userID, access)
		return CompletionCodeOK, nil
	case 0x02: // Set password
		if err := state.SetUserPassword(userID, password); err != nil {
			return CompletionCodeParameterOutOfRange, nil
		}
		return CompletionCodeOK, nil
	case 0x03: // Test password
		if state.CheckPassword(userID, password) {
			return CompletionCodeOK, nil
		}
		return CompletionCodeInvalidField, nil
	default:
		return CompletionCodeInvalidField, nil
	}
}

func handleSetUserAccess(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 2 {
		return CompletionCodeInvalidField, nil
	}

	channel := reqData[0] & 0x0F
	ipmiMsg := reqData[0]&0x10 != 0
	linkAuth := reqData[0]&0x20 != 0
	callin := reqData[0]&0x40 != 0

	userID := (reqData[1] >> 4) & 0x0F
	if len(reqData) > 2 {
		userID = reqData[1] & 0x3F
	}
	privLimit := reqData[1] & 0x0F

	// For simplicity, rebuild userID extraction
	userID = (reqData[1] >> 4) & 0x0F
	if userID == 0 && len(reqData) >= 2 {
		// Alternative parsing: user_id in bits 5:0, priv in bits 3:0 of byte 1
		userID = reqData[1] & 0x3F
		privLimit = reqData[1] & 0x0F
	}

	access := bmc.UserAccess{
		PrivilegeLimit: privLimit,
		Enabled:        true,
		IPMIMessaging:  ipmiMsg,
		LinkAuth:       linkAuth,
		CallinCallback: callin,
	}

	if err := state.SetUserAccess(channel, userID, access); err != nil {
		return CompletionCodeParameterOutOfRange, nil
	}
	return CompletionCodeOK, nil
}
```

**Note:** The `handleSetUserAccess` byte parsing above is a starting point. The IPMI spec defines:
- Byte 0: `[change_bits(1)][callin(1)][link_auth(1)][ipmi_msg(1)][channel(4)]`
- Byte 1: `[reserved(2)][user_id(6)]`
- Byte 2: `[reserved(4)][privilege_limit(4)]`

Adjust the parsing in Step 4 to match the spec precisely. The test verifies behavior, not byte layout.

### Step 5: Route new commands in handler_app.go

Add to `handleAppCommand` in `internal/ipmi/handler_app.go`:

The function signature needs to accept `*bmc.State`. This changes in Task 6 (dispatcher refactor). For now, implement the handlers as standalone functions.

### Step 6: Run tests to verify GREEN

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestHandle.*User -count=1`
Expected: All PASS

### Step 7: Commit

```bash
git add internal/ipmi/handler_user.go internal/ipmi/handler_user_test.go internal/ipmi/types.go
git commit -m "feat: add IPMI user management command handlers"
```

---

## Task 4: IPMI Channel Access & Info Command Handlers

**Files:**
- Create: `internal/ipmi/handler_channel.go`
- Create: `internal/ipmi/handler_channel_test.go`

### Step 1: Write failing tests

```go
// internal/ipmi/handler_channel_test.go
package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleGetChannelAccess(t *testing.T) {
	s := newTestBMCState()

	// Request: [channel(4bit)] [access_type(2bit):6=non-volatile]
	reqData := []byte{0x01, 0x40} // channel 1, non-volatile
	code, data := handleGetChannelAccess(reqData, s)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 2)

	accessMode := data[0] & 0x07
	assert.Equal(t, uint8(2), accessMode) // AlwaysAvailable

	privLimit := data[1] & 0x0F
	assert.Equal(t, uint8(4), privLimit) // Admin
}

func TestHandleSetChannelAccess(t *testing.T) {
	s := newTestBMCState()

	// Request: [channel(4bit)|flags(4bit)] [access_mode(4bit)] [priv_limit(4bit)]
	reqData := []byte{
		0x41, // channel=1, set non-volatile (bit 6)
		0x02, // access mode = AlwaysAvailable
		0x04, // privilege limit = Admin
	}
	code, _ := handleSetChannelAccess(reqData, s)
	assert.Equal(t, CompletionCodeOK, code)
}

func TestHandleGetChannelInfo(t *testing.T) {
	s := newTestBMCState()

	// Request: [channel_number]
	code, data := handleGetChannelInfo([]byte{0x01}, s)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 9)

	assert.Equal(t, uint8(1), data[0])    // channel number
	assert.Equal(t, uint8(0x04), data[1]) // medium: 802.3 LAN
	assert.Equal(t, uint8(0x01), data[2]) // protocol: IPMB-1.0
}

func TestHandleGetChannelInfo_CurrentChannel(t *testing.T) {
	s := newTestBMCState()

	// Channel 0x0E = current channel
	code, data := handleGetChannelInfo([]byte{0x0E}, s)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, data, 9)
	// Current channel resolves to channel 1
	assert.Equal(t, uint8(1), data[0])
}
```

### Step 2: Run tests to verify RED

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestHandle.*(Channel) -count=1`
Expected: FAIL

### Step 3: Implement channel handlers

```go
// internal/ipmi/handler_channel.go
package ipmi

import "github.com/steigr/qemu-bmc/internal/bmc"

func handleGetChannelAccess(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 2 {
		return CompletionCodeInvalidField, nil
	}

	channel := reqData[0] & 0x0F
	access := state.GetChannelAccess(channel)

	data := make([]byte, 2)
	// Byte 0: [alerting(1)][per_msg_auth(1)][user_level_auth(1)][access_mode(3)]
	data[0] = access.AccessMode & 0x07
	if access.UserLevelAuth {
		data[0] |= 0x10
	}
	if access.PerMsgAuth {
		data[0] |= 0x20
	}
	if access.AlertingEnabled {
		data[0] |= 0x40 // Note: alerting bit is inverted in spec (0=enabled)
	}

	// Byte 1: [reserved(4)][privilege_limit(4)]
	data[1] = access.PrivilegeLimit & 0x0F

	return CompletionCodeOK, data
}

func handleSetChannelAccess(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 3 {
		return CompletionCodeInvalidField, nil
	}

	channel := reqData[0] & 0x0F

	accessMode := reqData[1] & 0x07
	userLevelAuth := reqData[1]&0x10 != 0
	perMsgAuth := reqData[1]&0x20 != 0
	alerting := reqData[1]&0x40 != 0

	privLimit := reqData[2] & 0x0F

	access := bmc.ChannelAccess{
		AccessMode:      accessMode,
		UserLevelAuth:   userLevelAuth,
		PerMsgAuth:      perMsgAuth,
		AlertingEnabled: alerting,
		PrivilegeLimit:  privLimit,
	}

	state.SetChannelAccess(channel, access)
	return CompletionCodeOK, nil
}

func handleGetChannelInfo(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}

	channel := reqData[0] & 0x0F
	if channel == 0x0E {
		channel = 1 // Current channel → resolve to channel 1 (primary LAN)
	}

	info := state.GetChannelInfo(channel)

	data := make([]byte, 9)
	data[0] = info.ChannelNumber
	data[1] = info.ChannelMedium
	data[2] = info.ChannelProtocol
	data[3] = info.SessionSupport << 6 // session support in bits 7:6
	data[4] = 0x00 // Vendor ID byte 1
	data[5] = 0x00 // Vendor ID byte 2
	data[6] = 0x00 // Vendor ID byte 3
	data[7] = 0x00 // Aux channel info byte 1
	data[8] = 0x00 // Aux channel info byte 2

	return CompletionCodeOK, data
}
```

### Step 4: Run tests to verify GREEN

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestHandle.*(Channel) -count=1`
Expected: All PASS

### Step 5: Commit

```bash
git add internal/ipmi/handler_channel.go internal/ipmi/handler_channel_test.go
git commit -m "feat: add IPMI channel access and info command handlers"
```

---

## Task 5: IPMI LAN Configuration Command Handlers

**Files:**
- Create: `internal/ipmi/handler_lan.go`
- Create: `internal/ipmi/handler_lan_test.go`
- Modify: `internal/ipmi/types.go` (add NetFn Transport constants)

### Step 1: Add constants to types.go

```go
// IPMI Network Functions — add to existing block
const (
	NetFnTransport         = 0x0C
	NetFnTransportResponse = 0x0D
)

// IPMI Transport Commands
const (
	CmdSetLANConfigParams = 0x01
	CmdGetLANConfigParams = 0x02
)
```

### Step 2: Write failing tests

```go
// internal/ipmi/handler_lan_test.go
package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleGetLANConfigParams_IPAddress(t *testing.T) {
	s := newTestBMCState()
	s.SetLANConfig(3, []byte{192, 168, 1, 100})

	// Request: [channel] [param] [set_selector] [block_selector]
	reqData := []byte{0x01, 0x03, 0x00, 0x00}
	code, data := handleGetLANConfigParams(reqData, s)
	assert.Equal(t, CompletionCodeOK, code)
	require.True(t, len(data) >= 2)

	// Byte 0: parameter revision
	assert.Equal(t, uint8(0x11), data[0]) // revision 1.1

	// Bytes 1+: parameter data
	assert.Equal(t, []byte{192, 168, 1, 100}, data[1:])
}

func TestHandleGetLANConfigParams_AuthTypeSupport(t *testing.T) {
	s := newTestBMCState()

	reqData := []byte{0x01, 0x01, 0x00, 0x00} // param 1
	code, data := handleGetLANConfigParams(reqData, s)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, uint8(0x97), data[1]) // RMCP+ | Password | MD5 | MD2 | None
}

func TestHandleGetLANConfigParams_SubnetMask(t *testing.T) {
	s := newTestBMCState()
	s.SetLANConfig(6, []byte{255, 255, 255, 0})

	reqData := []byte{0x01, 0x06, 0x00, 0x00}
	code, data := handleGetLANConfigParams(reqData, s)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, []byte{255, 255, 255, 0}, data[1:])
}

func TestHandleSetLANConfigParams_IPAddress(t *testing.T) {
	s := newTestBMCState()

	// Request: [channel] [param] [data...]
	reqData := []byte{0x01, 0x03, 10, 0, 0, 50}
	code, _ := handleSetLANConfigParams(reqData, s)
	assert.Equal(t, CompletionCodeOK, code)

	ip := s.GetLANConfig(3)
	assert.Equal(t, []byte{10, 0, 0, 50}, ip)
}

func TestHandleSetLANConfigParams_AuthTypeEnables(t *testing.T) {
	s := newTestBMCState()

	// Param 2: 5 bytes of auth enables
	reqData := []byte{0x01, 0x02, 0x00, 0x14, 0x14, 0x14, 0x00}
	code, _ := handleSetLANConfigParams(reqData, s)
	assert.Equal(t, CompletionCodeOK, code)

	enables := s.GetLANConfig(2)
	assert.Equal(t, []byte{0x00, 0x14, 0x14, 0x14, 0x00}, enables)
}

func TestHandleGetLANConfigParams_SetInProgress(t *testing.T) {
	s := newTestBMCState()

	reqData := []byte{0x01, 0x00, 0x00, 0x00} // param 0
	code, data := handleGetLANConfigParams(reqData, s)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, uint8(0x00), data[1]) // set complete
}

func TestHandleGetLANConfigParams_UnknownParam(t *testing.T) {
	s := newTestBMCState()

	// Unknown parameter — should return parameter not supported
	reqData := []byte{0x01, 0xFE, 0x00, 0x00}
	code, _ := handleGetLANConfigParams(reqData, s)
	assert.Equal(t, CompletionCodeParameterOutOfRange, code)
}
```

### Step 3: Run tests to verify RED

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestHandle.*LAN -count=1`
Expected: FAIL

### Step 4: Implement LAN config handlers

```go
// internal/ipmi/handler_lan.go
package ipmi

import "github.com/steigr/qemu-bmc/internal/bmc"

// Supported LAN configuration parameters
var supportedLANParams = map[uint8]bool{
	0: true, 1: true, 2: true, 3: true, 4: true,
	5: true, 6: true, 7: true, 12: true, 13: true,
}

func handleGetLANConfigParams(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 4 {
		return CompletionCodeInvalidField, nil
	}

	param := reqData[1]

	// Param 0: Set In Progress
	if param == 0 {
		return CompletionCodeOK, []byte{0x11, 0x00} // revision 1.1, set complete
	}

	if !supportedLANParams[param] {
		return CompletionCodeParameterOutOfRange, nil
	}

	value := state.GetLANConfig(param)
	if value == nil {
		return CompletionCodeParameterOutOfRange, nil
	}

	// Response: [parameter_revision] [data...]
	data := make([]byte, 1+len(value))
	data[0] = 0x11 // Parameter revision 1.1
	copy(data[1:], value)

	return CompletionCodeOK, data
}

func handleSetLANConfigParams(reqData []byte, state *bmc.State) (CompletionCode, []byte) {
	if len(reqData) < 3 {
		return CompletionCodeInvalidField, nil
	}

	param := reqData[1]

	// Param 0: Set In Progress — accept but ignore
	if param == 0 {
		return CompletionCodeOK, nil
	}

	// Param 1: Auth Type Support — read-only
	if param == 1 {
		return CompletionCodeInvalidField, nil
	}

	if !supportedLANParams[param] {
		return CompletionCodeParameterOutOfRange, nil
	}

	state.SetLANConfig(param, reqData[2:])
	return CompletionCodeOK, nil
}
```

### Step 5: Run tests to verify GREEN

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestHandle.*LAN -count=1`
Expected: All PASS

### Step 6: Commit

```bash
git add internal/ipmi/handler_lan.go internal/ipmi/handler_lan_test.go internal/ipmi/types.go
git commit -m "feat: add IPMI LAN configuration command handlers"
```

---

## Task 6: Extend IPMI Command Dispatcher for BMC State

**Files:**
- Modify: `internal/ipmi/rmcp_plus.go` (update `handleIPMICommand` signature)
- Modify: `internal/ipmi/handler_app.go` (route new commands)
- Modify: `internal/ipmi/server.go` (add `*bmc.State` to Server, pass through)
- Modify: `internal/ipmi/types.go` (add `NetFnTransport`)
- Modify: existing tests to pass `*bmc.State`

This is a refactoring task that threads `*bmc.State` through the existing command dispatch chain.

### Step 1: Update `handleIPMICommand` signature

In `internal/ipmi/rmcp_plus.go`, change:

```go
// Before
func handleIPMICommand(msg *IPMIMessage, machine MachineInterface) (CompletionCode, []byte) {

// After
func handleIPMICommand(msg *IPMIMessage, machine MachineInterface, state *bmc.State) (CompletionCode, []byte) {
```

Add `NetFnTransport` case:

```go
func handleIPMICommand(msg *IPMIMessage, machine MachineInterface, state *bmc.State) (CompletionCode, []byte) {
	netFn := msg.GetNetFn()
	switch netFn {
	case NetFnApp:
		return handleAppCommand(msg, machine, state)
	case NetFnChassis:
		return handleChassisCommand(msg, machine)
	case NetFnTransport:
		return handleTransportCommand(msg, state)
	default:
		return CompletionCodeInvalidCommand, nil
	}
}
```

### Step 2: Update `handleAppCommand` to route new commands

In `internal/ipmi/handler_app.go`:

```go
func handleAppCommand(msg *IPMIMessage, machine MachineInterface, state *bmc.State) (CompletionCode, []byte) {
	switch msg.Command {
	// Existing commands...
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
	// New user management commands
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
	// Channel commands
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
```

### Step 3: Add transport command handler

Create `handleTransportCommand` (can live in `handler_lan.go`):

```go
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
```

### Step 4: Update Server struct

In `internal/ipmi/server.go`:

```go
type Server struct {
	machine    MachineInterface
	bmcState   *bmc.State
	sessionMgr *SessionManager
	user       string
	pass       string
	conn       net.PacketConn
}

func NewServer(m MachineInterface, state *bmc.State, user, pass string) *Server {
	return &Server{
		machine:    m,
		bmcState:   state,
		sessionMgr: NewSessionManager(),
		user:       user,
		pass:       pass,
	}
}
```

Update call site in `HandleMessage`:

```go
code, respData := handleIPMICommand(msg, s.machine, s.bmcState)
```

### Step 5: Update all call sites in rmcp_plus.go

Search for `handleIPMICommand(` in `rmcp_plus.go` (lines 317, 372) and add `state` parameter. The `HandleRMCPPlusMessage` function needs to receive `*bmc.State` too:

```go
func HandleRMCPPlusMessage(data []byte, mgr *SessionManager, user, pass string, machine MachineInterface, state *bmc.State) ([]byte, error) {
```

Update the call in `server.go:89`:

```go
resp, err := HandleRMCPPlusMessage(payload, s.sessionMgr, s.user, s.pass, s.machine, s.bmcState)
```

### Step 6: Update all existing tests

Update all test files that call `handleIPMICommand`, `handleAppCommand`, `HandleRMCPPlusMessage`, or `NewServer` to pass a `*bmc.State`. For tests that don't use BMC state, pass `bmc.NewState("admin", "password")`.

### Step 7: Run full test suite

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -count=1`
Expected: All PASS

### Step 8: Run entire project test suite

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./... -count=1`
Expected: All PASS (main.go will need updating too — see Task 9)

### Step 9: Commit

```bash
git add internal/ipmi/
git commit -m "refactor: thread BMC state through IPMI command dispatcher"
```

---

## Task 7: OpenIPMI VM Wire Protocol

**Files:**
- Create: `internal/ipmi/vm_protocol.go`
- Create: `internal/ipmi/vm_protocol_test.go`

Implements the byte-level wire protocol used by QEMU's `ipmi-bmc-extern` chardev.

### Step 1: Write failing tests

```go
// internal/ipmi/vm_protocol_test.go
package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVMProtocol_EscapeBytes(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect []byte
	}{
		{"no escaping needed", []byte{0x01, 0x02, 0x03}, []byte{0x01, 0x02, 0x03}},
		{"escape 0xA0", []byte{0xA0}, []byte{0xAA, 0xB0}},
		{"escape 0xA1", []byte{0xA1}, []byte{0xAA, 0xB1}},
		{"escape 0xAA", []byte{0xAA}, []byte{0xAA, 0xBA}},
		{"mixed", []byte{0x01, 0xA0, 0x02, 0xAA}, []byte{0x01, 0xAA, 0xB0, 0x02, 0xAA, 0xBA}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := vmEscapeBytes(tt.input)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestVMProtocol_UnescapeBytes(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		expect []byte
	}{
		{"no escaping", []byte{0x01, 0x02}, []byte{0x01, 0x02}},
		{"unescape 0xB0", []byte{0xAA, 0xB0}, []byte{0xA0}},
		{"unescape 0xB1", []byte{0xAA, 0xB1}, []byte{0xA1}},
		{"unescape 0xBA", []byte{0xAA, 0xBA}, []byte{0xAA}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := vmUnescapeBytes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestVMProtocol_UnescapeBytes_TrailingEscape(t *testing.T) {
	_, err := vmUnescapeBytes([]byte{0x01, 0xAA})
	assert.Error(t, err)
}

func TestVMProtocol_VMChecksum(t *testing.T) {
	// Same two's complement checksum as IPMI
	data := []byte{0x06, 0x01} // seq=0x06, netfn/lun, ...
	cs := vmChecksum(data)
	expected := uint8(0x100 - ((0x06 + 0x01) & 0xFF))
	assert.Equal(t, expected, cs)
}

func TestVMProtocol_ParseIPMIMessage(t *testing.T) {
	// Construct: [seq] [netfn<<2|lun] [cmd] [data...] [checksum]
	seq := uint8(0x01)
	netfnLun := uint8(0x06<<2 | 0x00) // NetFn App, LUN 0
	cmd := uint8(0x01)                 // Get Device ID
	body := []byte{seq, netfnLun, cmd}
	cs := vmChecksum(body)
	raw := append(body, cs)

	msg, err := vmParseIPMIRequest(raw)
	require.NoError(t, err)
	assert.Equal(t, seq, msg.Seq)
	assert.Equal(t, uint8(0x06), msg.NetFn)
	assert.Equal(t, uint8(0x00), msg.LUN)
	assert.Equal(t, cmd, msg.Cmd)
	assert.Empty(t, msg.Data)
}

func TestVMProtocol_ParseIPMIMessage_WithData(t *testing.T) {
	seq := uint8(0x05)
	netfnLun := uint8(0x06<<2 | 0x00) // NetFn App
	cmd := uint8(0x38)                 // Get Channel Auth Capabilities
	reqData := []byte{0x01, 0x04}      // channel 1, priv level 4
	body := append([]byte{seq, netfnLun, cmd}, reqData...)
	cs := vmChecksum(body)
	raw := append(body, cs)

	msg, err := vmParseIPMIRequest(raw)
	require.NoError(t, err)
	assert.Equal(t, uint8(0x38), msg.Cmd)
	assert.Equal(t, reqData, msg.Data)
}

func TestVMProtocol_ParseIPMIMessage_BadChecksum(t *testing.T) {
	raw := []byte{0x01, 0x18, 0x01, 0xFF} // bad checksum
	_, err := vmParseIPMIRequest(raw)
	assert.Error(t, err)
}

func TestVMProtocol_BuildIPMIResponse(t *testing.T) {
	resp := vmBuildIPMIResponse(0x01, 0x07, 0x00, 0x01, CompletionCodeOK, []byte{0x20, 0x01})

	// [seq] [netfn<<2|lun] [cmd] [cc] [data...] [checksum]
	assert.Equal(t, uint8(0x01), resp[0])   // seq
	assert.Equal(t, uint8(0x07<<2), resp[1]) // NetFn App Response
	assert.Equal(t, uint8(0x01), resp[2])   // cmd
	assert.Equal(t, uint8(0x00), resp[3])   // completion code OK
	assert.Equal(t, uint8(0x20), resp[4])   // data
	assert.Equal(t, uint8(0x01), resp[5])   // data

	// Verify checksum
	cs := vmChecksum(resp[:len(resp)-1])
	assert.Equal(t, cs, resp[len(resp)-1])
}

func TestVMProtocol_ParseControlCommand(t *testing.T) {
	// Version command: [0xFF] [0x01]
	cmd, data, err := vmParseControlCommand([]byte{0xFF, 0x01})
	require.NoError(t, err)
	assert.Equal(t, uint8(0xFF), cmd)
	assert.Equal(t, []byte{0x01}, data)
}

func TestVMProtocol_ParseCapabilities(t *testing.T) {
	// Capabilities: [0x08] [capability_flags]
	cmd, data, err := vmParseControlCommand([]byte{0x08, 0x3F})
	require.NoError(t, err)
	assert.Equal(t, VMCmdCapabilities, cmd)
	assert.Equal(t, []byte{0x3F}, data)
}

func TestVMProtocol_BuildControlCommand(t *testing.T) {
	// Build NOATTN command
	raw := vmBuildControlCommand(VMCmdNoAttn)
	// Should be [0x00] (no data)
	assert.Equal(t, []byte{0x00}, raw)
}

func TestVMProtocol_BuildPoweroffCommand(t *testing.T) {
	raw := vmBuildControlCommand(VMCmdPowerOff)
	assert.Equal(t, []byte{0x03}, raw)
}
```

### Step 2: Run tests to verify RED

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestVMProtocol -count=1`
Expected: FAIL

### Step 3: Implement VM wire protocol

```go
// internal/ipmi/vm_protocol.go
package ipmi

import "fmt"

// VM protocol framing characters
const (
	VMMsgChar    = 0xA0 // End of IPMI message
	VMCmdChar    = 0xA1 // End of control command
	VMEscapeChar = 0xAA // Escape: next byte has bit 4 set
)

// VM hardware control commands (BMC → VM)
const (
	VMCmdNoAttn            = 0x00
	VMCmdAttn              = 0x01
	VMCmdAttnIRQ           = 0x02
	VMCmdPowerOff          = 0x03
	VMCmdReset             = 0x04
	VMCmdEnableIRQ         = 0x05
	VMCmdDisableIRQ        = 0x06
	VMCmdSendNMI           = 0x07
	VMCmdCapabilities      = 0x08
	VMCmdGracefulShutdown  = 0x09
)

// VM protocol version
const (
	VMCmdVersion  = 0xFF
	VMProtocolVer = 0x01
)

// VM capability flags
const (
	VMCapPower            = 0x01
	VMCapReset            = 0x02
	VMCapIRQ              = 0x04
	VMCapNMI              = 0x08
	VMCapAttn             = 0x10
	VMCapGracefulShutdown = 0x20
)

// VMIPMIRequest is a parsed IPMI request from the VM wire protocol
type VMIPMIRequest struct {
	Seq   uint8
	NetFn uint8
	LUN   uint8
	Cmd   uint8
	Data  []byte
}

// vmEscapeBytes escapes special bytes in the VM protocol
func vmEscapeBytes(data []byte) []byte {
	var result []byte
	for _, b := range data {
		if b == VMMsgChar || b == VMCmdChar || b == VMEscapeChar {
			result = append(result, VMEscapeChar, b|0x10)
		} else {
			result = append(result, b)
		}
	}
	return result
}

// vmUnescapeBytes reverses VM protocol byte escaping
func vmUnescapeBytes(data []byte) ([]byte, error) {
	var result []byte
	for i := 0; i < len(data); i++ {
		if data[i] == VMEscapeChar {
			i++
			if i >= len(data) {
				return nil, fmt.Errorf("trailing escape byte")
			}
			result = append(result, data[i]&^0x10)
		} else {
			result = append(result, data[i])
		}
	}
	return result, nil
}

// vmChecksum computes the two's complement checksum for the VM protocol
func vmChecksum(data []byte) uint8 {
	var sum uint32
	for _, b := range data {
		sum += uint32(b)
	}
	return uint8(0x100 - (sum & 0xFF))
}

// vmParseIPMIRequest parses an IPMI request from unescaped VM protocol bytes
// Format: [seq] [netfn<<2|lun] [cmd] [data...] [checksum]
func vmParseIPMIRequest(data []byte) (*VMIPMIRequest, error) {
	if len(data) < 4 { // minimum: seq + netfn/lun + cmd + checksum
		return nil, fmt.Errorf("VM IPMI message too short: %d bytes", len(data))
	}

	// Verify checksum
	cs := vmChecksum(data[:len(data)-1])
	if cs != data[len(data)-1] {
		return nil, fmt.Errorf("VM IPMI checksum mismatch: got 0x%02x, want 0x%02x", data[len(data)-1], cs)
	}

	msg := &VMIPMIRequest{
		Seq:   data[0],
		NetFn: (data[1] >> 2) & 0x3F,
		LUN:   data[1] & 0x03,
		Cmd:   data[2],
	}

	if len(data) > 4 {
		msg.Data = make([]byte, len(data)-4)
		copy(msg.Data, data[3:len(data)-1])
	}

	return msg, nil
}

// vmBuildIPMIResponse builds an IPMI response for the VM wire protocol
// Format: [seq] [netfn<<2|lun] [cmd] [cc] [data...] [checksum]
func vmBuildIPMIResponse(seq, netFn, lun, cmd uint8, cc CompletionCode, data []byte) []byte {
	body := []byte{seq, (netFn << 2) | (lun & 0x03), cmd, uint8(cc)}
	body = append(body, data...)
	cs := vmChecksum(body)
	return append(body, cs)
}

// vmParseControlCommand parses a VM control command
// Format: [command_code] [data...]
func vmParseControlCommand(data []byte) (uint8, []byte, error) {
	if len(data) < 1 {
		return 0, nil, fmt.Errorf("empty control command")
	}
	return data[0], data[1:], nil
}

// vmBuildControlCommand builds a VM control command (for sending to QEMU)
func vmBuildControlCommand(cmd uint8, data ...byte) []byte {
	result := []byte{cmd}
	result = append(result, data...)
	return result
}
```

### Step 4: Run tests to verify GREEN

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestVMProtocol -count=1`
Expected: All PASS

### Step 5: Commit

```bash
git add internal/ipmi/vm_protocol.go internal/ipmi/vm_protocol_test.go
git commit -m "feat: implement OpenIPMI VM wire protocol encoding/decoding"
```

---

## Task 8: VM Protocol Server

**Files:**
- Create: `internal/ipmi/vm_server.go`
- Create: `internal/ipmi/vm_server_test.go`

TCP server that accepts connections from QEMU's `ipmi-bmc-extern` chardev, speaks the VM wire protocol, and routes IPMI commands to the shared handler.

### Step 1: Write failing tests

```go
// internal/ipmi/vm_server_test.go
package ipmi

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/bmc"
)

// mockVMConn simulates the QEMU side of the chardev connection
type mockVMConn struct {
	server net.Conn
	client net.Conn
}

func newMockVMConn(t *testing.T) *mockVMConn {
	t.Helper()
	server, client := net.Pipe()
	return &mockVMConn{server: server, client: client}
}

func (m *mockVMConn) close() {
	m.server.Close()
	m.client.Close()
}

// sendAndReceive sends raw bytes and reads the response from the VM server
func (m *mockVMConn) sendRaw(t *testing.T, data []byte) {
	t.Helper()
	_, err := m.client.Write(data)
	require.NoError(t, err)
}

func (m *mockVMConn) readRaw(t *testing.T, timeout time.Duration) []byte {
	t.Helper()
	m.client.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 1024)
	n, err := m.client.Read(buf)
	require.NoError(t, err)
	return buf[:n]
}

func TestVMServer_Handshake(t *testing.T) {
	mock := newMockVMConn(t)
	defer mock.close()

	state := bmc.NewState("admin", "password")
	machine := &ipmiMockMachine{}
	vs := NewVMServer(machine, state)

	// Start handling connection in background
	done := make(chan error, 1)
	go func() {
		done <- vs.HandleConnection(mock.server)
	}()

	// QEMU sends version command
	mock.sendRaw(t, []byte{0xFF, 0x01, VMCmdChar})

	// QEMU sends capabilities
	caps := uint8(VMCapPower | VMCapReset | VMCapAttn | VMCapGracefulShutdown)
	mock.sendRaw(t, []byte{VMCmdCapabilities, caps, VMCmdChar})

	// Read response (server sends NOATTN after handshake)
	resp := mock.readRaw(t, time.Second)
	// Should contain NOATTN command terminated by VMCmdChar
	assert.Contains(t, resp, byte(VMCmdChar))

	mock.close()
	<-done
}

func TestVMServer_GetDeviceID(t *testing.T) {
	mock := newMockVMConn(t)
	defer mock.close()

	state := bmc.NewState("admin", "password")
	machine := &ipmiMockMachine{}
	vs := NewVMServer(machine, state)

	done := make(chan error, 1)
	go func() {
		done <- vs.HandleConnection(mock.server)
	}()

	// Complete handshake
	mock.sendRaw(t, []byte{0xFF, 0x01, VMCmdChar})
	mock.sendRaw(t, []byte{VMCmdCapabilities, 0x3F, VMCmdChar})
	// Read and discard handshake response
	mock.readRaw(t, time.Second)

	// Send Get Device ID request
	// [seq=1] [netfn=App(0x06)<<2|lun=0] [cmd=0x01] [checksum]
	seq := uint8(0x01)
	netfnLun := uint8(0x06<<2 | 0x00)
	cmd := uint8(0x01)
	body := []byte{seq, netfnLun, cmd}
	cs := vmChecksum(body)
	raw := append(body, cs)
	escaped := vmEscapeBytes(raw)
	mock.sendRaw(t, append(escaped, VMMsgChar))

	// Read response
	resp := mock.readRaw(t, time.Second)

	// Find the IPMI message (terminated by VMMsgChar)
	// Unescape and parse
	var msgBytes []byte
	for _, b := range resp {
		if b == VMMsgChar {
			break
		}
		msgBytes = append(msgBytes, b)
	}

	unescaped, err := vmUnescapeBytes(msgBytes)
	require.NoError(t, err)

	// Parse: [seq] [netfn_resp<<2|lun] [cmd] [cc] [data...] [checksum]
	require.True(t, len(unescaped) >= 5)
	assert.Equal(t, seq, unescaped[0])                    // echo seq
	assert.Equal(t, uint8(0x07<<2), unescaped[1])         // App Response NetFn
	assert.Equal(t, uint8(0x01), unescaped[2])            // cmd
	assert.Equal(t, uint8(CompletionCodeOK), unescaped[3]) // success

	mock.close()
	<-done
}

func TestVMServer_GetUserName(t *testing.T) {
	mock := newMockVMConn(t)
	defer mock.close()

	state := bmc.NewState("admin", "password")
	machine := &ipmiMockMachine{}
	vs := NewVMServer(machine, state)

	done := make(chan error, 1)
	go func() {
		done <- vs.HandleConnection(mock.server)
	}()

	// Handshake
	mock.sendRaw(t, []byte{0xFF, 0x01, VMCmdChar})
	mock.sendRaw(t, []byte{VMCmdCapabilities, 0x3F, VMCmdChar})
	mock.readRaw(t, time.Second)

	// Send Get User Name for user 2
	seq := uint8(0x02)
	netfnLun := uint8(0x06<<2 | 0x00) // App
	cmd := uint8(CmdGetUserName)
	userData := []byte{0x02} // user ID 2
	body := append([]byte{seq, netfnLun, cmd}, userData...)
	cs := vmChecksum(body)
	raw := append(body, cs)
	escaped := vmEscapeBytes(raw)
	mock.sendRaw(t, append(escaped, VMMsgChar))

	// Read response
	resp := mock.readRaw(t, time.Second)
	var msgBytes []byte
	for _, b := range resp {
		if b == VMMsgChar {
			break
		}
		msgBytes = append(msgBytes, b)
	}
	unescaped, err := vmUnescapeBytes(msgBytes)
	require.NoError(t, err)
	require.True(t, len(unescaped) >= 5)
	assert.Equal(t, uint8(CompletionCodeOK), unescaped[3])

	// Data starts at index 4, ends before checksum
	nameData := unescaped[4 : len(unescaped)-1]
	assert.Equal(t, "admin", string(nameData[:5]))

	mock.close()
	<-done
}
```

### Step 2: Run tests to verify RED

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestVMServer -count=1`
Expected: FAIL

### Step 3: Implement VM server

```go
// internal/ipmi/vm_server.go
package ipmi

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	"github.com/steigr/qemu-bmc/internal/bmc"
)

// VMServer handles the OpenIPMI VM wire protocol for ipmi-bmc-extern
type VMServer struct {
	machine  MachineInterface
	bmcState *bmc.State
	listener net.Listener
	mu       sync.Mutex
	vmCaps   uint8 // capabilities reported by QEMU
}

// NewVMServer creates a new VM protocol server
func NewVMServer(machine MachineInterface, state *bmc.State) *VMServer {
	return &VMServer{
		machine:  machine,
		bmcState: state,
	}
}

// ListenAndServe starts listening for chardev connections from QEMU
func (vs *VMServer) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("VM protocol listen on %s: %w", addr, err)
	}
	vs.listener = ln
	log.Printf("VM protocol server listening on %s", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return fmt.Errorf("VM protocol accept: %w", err)
		}
		log.Printf("VM protocol: connection from %s", conn.RemoteAddr())
		go func() {
			if err := vs.HandleConnection(conn); err != nil {
				log.Printf("VM protocol connection error: %v", err)
			}
		}()
	}
}

// HandleConnection processes a single chardev connection
func (vs *VMServer) HandleConnection(conn net.Conn) error {
	defer conn.Close()

	reader := &vmReader{conn: conn}

	for {
		msgType, data, err := reader.ReadMessage()
		if err != nil {
			if err == io.EOF {
				log.Println("VM protocol: connection closed")
				return nil
			}
			return fmt.Errorf("reading VM message: %w", err)
		}

		switch msgType {
		case VMCmdChar:
			// Hardware control command from QEMU
			vs.handleControlCommand(data, conn)

		case VMMsgChar:
			// IPMI message from guest
			vs.handleIPMIMsg(data, conn)
		}
	}
}

func (vs *VMServer) handleControlCommand(data []byte, conn net.Conn) {
	cmd, cmdData, err := vmParseControlCommand(data)
	if err != nil {
		log.Printf("VM protocol: bad control command: %v", err)
		return
	}

	switch cmd {
	case VMCmdVersion:
		if len(cmdData) > 0 {
			log.Printf("VM protocol: QEMU version %d", cmdData[0])
		}
	case VMCmdCapabilities:
		if len(cmdData) > 0 {
			vs.mu.Lock()
			vs.vmCaps = cmdData[0]
			vs.mu.Unlock()
			log.Printf("VM protocol: QEMU capabilities 0x%02x", cmdData[0])
		}
		// Send NOATTN to indicate ready
		vs.sendControlCommand(conn, VMCmdNoAttn)
	default:
		log.Printf("VM protocol: unknown control command 0x%02x", cmd)
	}
}

func (vs *VMServer) handleIPMIMsg(data []byte, conn net.Conn) {
	req, err := vmParseIPMIRequest(data)
	if err != nil {
		log.Printf("VM protocol: bad IPMI message: %v", err)
		return
	}

	log.Printf("VM protocol: IPMI request seq=%d netfn=0x%02x cmd=0x%02x", req.Seq, req.NetFn, req.Cmd)

	// Convert to IPMIMessage for the shared handler
	msg := &IPMIMessage{
		TargetLun: (req.NetFn << 2) | (req.LUN & 0x03),
		Command:   req.Cmd,
		Data:      req.Data,
	}

	code, respData := handleIPMICommand(msg, vs.machine, vs.bmcState)

	// Build and send response
	respNetFn := req.NetFn | 0x01 // response NetFn
	resp := vmBuildIPMIResponse(req.Seq, respNetFn, req.LUN, req.Cmd, code, respData)
	escaped := vmEscapeBytes(resp)
	frame := append(escaped, VMMsgChar)

	if _, err := conn.Write(frame); err != nil {
		log.Printf("VM protocol: write error: %v", err)
	}
}

func (vs *VMServer) sendControlCommand(conn net.Conn, cmd uint8, data ...byte) {
	raw := vmBuildControlCommand(cmd, data...)
	escaped := vmEscapeBytes(raw)
	frame := append(escaped, VMCmdChar)
	if _, err := conn.Write(frame); err != nil {
		log.Printf("VM protocol: control write error: %v", err)
	}
}

// Close stops the VM server
func (vs *VMServer) Close() error {
	if vs.listener != nil {
		return vs.listener.Close()
	}
	return nil
}

// vmReader reads framed messages from the VM protocol stream
type vmReader struct {
	conn net.Conn
	buf  []byte
}

// ReadMessage reads the next complete message from the stream
// Returns the message type (VMMsgChar or VMCmdChar) and unescaped data
func (r *vmReader) ReadMessage() (byte, []byte, error) {
	oneByte := make([]byte, 1)
	var raw []byte

	for {
		_, err := io.ReadFull(r.conn, oneByte)
		if err != nil {
			return 0, nil, err
		}

		b := oneByte[0]
		if b == VMMsgChar || b == VMCmdChar {
			// End of message
			data, err := vmUnescapeBytes(raw)
			if err != nil {
				return 0, nil, fmt.Errorf("unescaping VM message: %w", err)
			}
			return b, data, nil
		}

		raw = append(raw, b)
	}
}
```

### Step 4: Run tests to verify GREEN

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -run TestVMServer -count=1`
Expected: All PASS

### Step 5: Commit

```bash
git add internal/ipmi/vm_server.go internal/ipmi/vm_server_test.go
git commit -m "feat: add VM protocol server for ipmi-bmc-extern chardev"
```

---

## Task 9: Configuration & Main Integration

**Files:**
- Modify: `internal/config/config.go` (add `VMIPMIAddr` env var)
- Modify: `internal/config/config_test.go`
- Modify: `cmd/qemu-bmc/main.go` (create BMCState, start VM server)

### Step 1: Add `VMIPMIAddr` to Config

In `internal/config/config.go`, add field to `Config`:

```go
type Config struct {
	// ... existing fields ...
	VMIPMIAddr string // VM IPMI chardev listen address (e.g., ":9002")
}
```

Add to `Load()`:

```go
VMIPMIAddr: getEnv("VM_IPMI_ADDR", ""),  // Empty = disabled
```

### Step 2: Write config test

```go
// Add to config_test.go
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
```

### Step 3: Run config tests

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/config/... -v -count=1`
Expected: All PASS

### Step 4: Update main.go

```go
package main

import (
	// ... existing imports ...
	"github.com/steigr/qemu-bmc/internal/bmc"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("qemu-bmc starting...")

	cfg := config.Load()

	// Connect to QMP
	qmpClient, err := qmp.NewClient(cfg.QMPSocket)
	if err != nil {
		log.Fatalf("Failed to connect to QMP socket %s: %v", cfg.QMPSocket, err)
	}
	defer qmpClient.Close()
	log.Println("Connected to QMP socket")

	// Create machine
	m := machine.New(qmpClient)

	// Create shared BMC state
	bmcState := bmc.NewState(cfg.IPMIUser, cfg.IPMIPass)

	// Start IPMI server (out-of-band, UDP)
	ipmiServer := ipmi.NewServer(m, bmcState, cfg.IPMIUser, cfg.IPMIPass)
	go func() {
		addr := fmt.Sprintf(":%s", cfg.IPMIPort)
		log.Printf("Starting IPMI server on %s", addr)
		if err := ipmiServer.ListenAndServe(addr); err != nil {
			log.Fatalf("IPMI server error: %v", err)
		}
	}()

	// Start VM IPMI server (in-band, TCP chardev) if configured
	if cfg.VMIPMIAddr != "" {
		vmServer := ipmi.NewVMServer(m, bmcState)
		go func() {
			log.Printf("Starting VM IPMI server on %s", cfg.VMIPMIAddr)
			if err := vmServer.ListenAndServe(cfg.VMIPMIAddr); err != nil {
				log.Fatalf("VM IPMI server error: %v", err)
			}
		}()
	}

	// Start Redfish server (unchanged)
	// ...
}
```

### Step 5: Build and verify

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go build ./cmd/qemu-bmc`
Expected: Build succeeds

### Step 6: Run full test suite

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./... -count=1 && go vet ./...`
Expected: All PASS, no vet issues

### Step 7: Commit

```bash
git add internal/config/config.go internal/config/config_test.go cmd/qemu-bmc/main.go
git commit -m "feat: integrate VM IPMI server with main entrypoint"
```

---

## Task 10: Update IPMI LAN Auth to Use BMC State (Optional Enhancement)

**Files:**
- Modify: `internal/ipmi/rmcp_plus.go` (RAKP auth checks)
- Modify: `internal/ipmi/handler_app.go` (session challenge)

For full end-to-end MaaS commissioning, users created via in-band IPMI (Task 3) must be able to authenticate over out-of-band IPMI (LAN). This task updates the RMCP+ authentication flow to validate credentials against `*bmc.State` instead of the hardcoded user/pass.

### Step 1: Write failing test

```go
// Add to rmcp_plus_test.go
func TestRMCPPlus_AuthWithBMCStateUser(t *testing.T) {
	state := bmc.NewState("admin", "password")

	// Create a new user via BMC state (simulating in-band creation)
	state.SetUserName(3, "maas")
	state.SetUserPassword(3, "maas-secret")
	state.SetUserAccess(1, 3, bmc.UserAccess{Enabled: true, PrivilegeLimit: 4, IPMIMessaging: true, LinkAuth: true})

	// Attempt RAKP authentication with the new user
	// ... (full RMCP+ auth flow test with user "maas" / "maas-secret")
	// The test verifies that RAKP Message 2 succeeds with the correct HMAC
}
```

### Step 2: Update RAKP auth to look up users in BMCState

In `rmcp_plus.go`, modify the RAKP Message 1 handler to:
1. Look up the username in `*bmc.State` via `LookupUserByName()`
2. Retrieve the password for HMAC calculation
3. Verify user is enabled and has sufficient privileges

The `user` and `pass` string fields on `Server` become the fallback (or can be removed if BMCState is the single source of truth).

### Step 3: Run tests

Run: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin && go test ./internal/ipmi/... -v -count=1`
Expected: All PASS

### Step 4: Commit

```bash
git add internal/ipmi/rmcp_plus.go internal/ipmi/rmcp_plus_test.go
git commit -m "feat: authenticate IPMI LAN sessions against BMC state users"
```

---

## Task 11: Documentation & QEMU Usage Guide

**Files:**
- Modify: `CLAUDE.md` (add VM IPMI section)
- Modify: `README.md` (add usage instructions)

### Step 1: Update CLAUDE.md

Add the `VM_IPMI_ADDR` environment variable to the table and document the new architecture.

### Step 2: Update README.md

Add QEMU configuration example showing how to use `ipmi-bmc-extern`:

```bash
# Start qemu-bmc with VM IPMI support
VM_IPMI_ADDR=:9002 ./qemu-bmc

# Start QEMU with ipmi-bmc-extern
qemu-system-x86_64 \
  -chardev socket,id=ipmi0,host=localhost,port=9002,reconnect=10 \
  -device ipmi-bmc-extern,id=bmc0,chardev=ipmi0 \
  -device isa-ipmi-kcs,bmc=bmc0 \
  ...
```

### Step 3: Commit

```bash
git add CLAUDE.md README.md
git commit -m "docs: add VM IPMI configuration and usage guide"
```

---

## Summary

| Task | Description | Dependencies |
|------|-------------|--------------|
| 1 | BMC State — User accounts | None |
| 2 | BMC State — LAN config & channel access | Task 1 |
| 3 | IPMI User Management handlers | Tasks 1, 2 |
| 4 | IPMI Channel Access handlers | Tasks 1, 2 |
| 5 | IPMI LAN Configuration handlers | Tasks 1, 2 |
| 6 | Extend IPMI command dispatcher | Tasks 3, 4, 5 |
| 7 | VM Wire Protocol | None (parallel with 1-6) |
| 8 | VM Protocol Server | Tasks 6, 7 |
| 9 | Configuration & Main integration | Tasks 6, 8 |
| 10 | LAN auth via BMC State (optional) | Task 9 |
| 11 | Documentation | Task 9 |

**Parallelizable:** Tasks 1-2 and Task 7 can be developed in parallel. Tasks 3, 4, 5 can be developed in parallel after Tasks 1-2.

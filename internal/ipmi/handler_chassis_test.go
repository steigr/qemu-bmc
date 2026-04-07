package ipmi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/machine"
)

func TestGetChassisStatus_PowerOn(t *testing.T) {
	mock := newIPMIMockMachine(machine.PowerOn)
	msg := &IPMIMessage{Command: CmdGetChassisStatus}
	code, data := handleChassisCommand(msg, mock)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, byte(0x01), data[0]&0x01) // power on
}

func TestGetChassisStatus_PowerOff(t *testing.T) {
	mock := newIPMIMockMachine(machine.PowerOff)
	msg := &IPMIMessage{Command: CmdGetChassisStatus}
	code, data := handleChassisCommand(msg, mock)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, byte(0x00), data[0]&0x01) // power off
}

func TestChassisControl(t *testing.T) {
	tests := []struct {
		name      string
		control   byte
		initState machine.PowerState
		wantCalls []string
	}{
		{"PowerOff", ChassisControlPowerDown, machine.PowerOn, []string{"ForceOff"}},
		{"PowerOn", ChassisControlPowerUp, machine.PowerOff, []string{"On"}},
		{"PowerCycle", ChassisControlPowerCycle, machine.PowerOn, []string{"ForceOff", "On"}},
		{"HardReset", ChassisControlHardReset, machine.PowerOn, []string{"ForceRestart"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newIPMIMockMachine(tt.initState)
			msg := &IPMIMessage{
				Command: CmdChassisControl,
				Data:    []byte{tt.control},
			}
			code, _ := handleChassisCommand(msg, mock)
			assert.Equal(t, CompletionCodeOK, code)
			assert.Equal(t, tt.wantCalls, mock.calls)
		})
	}
}

func TestSetBootOptions_PXE(t *testing.T) {
	mock := newIPMIMockMachine(machine.PowerOn)
	// Parameter 5 (boot flags): valid=1, UEFI=1, device=PXE(0x01)
	data := []byte{
		0x05,       // parameter selector
		0xA0,       // valid(0x80) + UEFI(0x20)
		0x04,       // PXE (0x01 << 2)
		0x00, 0x00, 0x00,
	}
	msg := &IPMIMessage{Command: CmdSetBootOptions, Data: data}
	code, _ := handleChassisCommand(msg, mock)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, "Once", mock.bootOverride.Enabled)
	assert.Equal(t, "Pxe", mock.bootOverride.Target)
	assert.Equal(t, "UEFI", mock.bootOverride.Mode)
}

func TestSetBootOptions_HDD(t *testing.T) {
	mock := newIPMIMockMachine(machine.PowerOn)
	data := []byte{
		0x05, 0xA0,
		0x08, // HDD (0x02 << 2)
		0x00, 0x00, 0x00,
	}
	msg := &IPMIMessage{Command: CmdSetBootOptions, Data: data}
	code, _ := handleChassisCommand(msg, mock)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, "Hdd", mock.bootOverride.Target)
}

func TestGetBootOptions(t *testing.T) {
	mock := newIPMIMockMachine(machine.PowerOn)
	mock.bootOverride = machine.BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"}

	data := []byte{0x05, 0x00, 0x00}
	msg := &IPMIMessage{Command: CmdGetBootOptions, Data: data}
	code, resp := handleChassisCommand(msg, mock)
	assert.Equal(t, CompletionCodeOK, code)
	require.Len(t, resp, 7)
	assert.Equal(t, byte(0x01), resp[0]) // parameter version
	assert.Equal(t, byte(0x85), resp[1]) // valid + param selector 5
	assert.Equal(t, byte(0xA0), resp[2]) // boot flags valid + UEFI
	assert.Equal(t, byte(0x04), resp[3]) // PXE (0x01 << 2)
}

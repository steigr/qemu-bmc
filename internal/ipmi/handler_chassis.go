package ipmi

import (
	"log"

	"github.com/steigr/qemu-bmc/internal/machine"
)

// handleChassisCommand handles Chassis network function commands
func handleChassisCommand(msg *IPMIMessage, m MachineInterface) (CompletionCode, []byte) {
	switch msg.Command {
	case CmdGetChassisStatus:
		return handleGetChassisStatus(m)
	case CmdChassisControl:
		return handleChassisControl(msg.Data, m)
	case CmdChassisIdentify:
		return handleChassisIdentify()
	case CmdSetBootOptions:
		return handleSetBootOptions(msg.Data, m)
	case CmdGetBootOptions:
		return handleGetBootOptions(msg.Data, m)
	default:
		return CompletionCodeInvalidCommand, nil
	}
}

func handleGetChassisStatus(m MachineInterface) (CompletionCode, []byte) {
	state, err := m.GetPowerState()
	if err != nil {
		return CompletionCodeUnspecified, nil
	}

	var powerByte byte
	if state == machine.PowerOn {
		powerByte = 0x01 // bit 0 = power on
	}

	data := []byte{
		powerByte, // Current Power State
		0x00,      // Last Power Event
		0x00,      // Misc Chassis State
		0x00,      // Front Panel Button
	}
	return CompletionCodeOK, data
}

func handleChassisControl(reqData []byte, m MachineInterface) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}

	control := reqData[0]
	var err error

	switch control {
	case ChassisControlPowerDown:
		err = m.Reset("ForceOff")
	case ChassisControlPowerUp:
		err = m.Reset("On")
	case ChassisControlPowerCycle:
		err = m.Reset("ForceOff")
		if err == nil {
			err = m.Reset("On")
		}
	case ChassisControlHardReset:
		err = m.Reset("ForceRestart")
	case ChassisControlPulse, ChassisControlSoftOff:
		// No-op for virtual BMC
		log.Printf("IPMI: chassis control 0x%02x (no-op)", control)
	default:
		return CompletionCodeInvalidField, nil
	}

	if err != nil {
		return CompletionCodeUnspecified, nil
	}
	return CompletionCodeOK, nil
}

func handleChassisIdentify() (CompletionCode, []byte) {
	log.Println("IPMI: Chassis Identify requested")
	return CompletionCodeOK, nil
}

func handleSetBootOptions(reqData []byte, m MachineInterface) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}

	paramSelector := reqData[0] & 0x7F

	switch paramSelector {
	case 5: // Boot flags
		if len(reqData) < 6 {
			return CompletionCodeInvalidField, nil
		}
		bootFlags := reqData[1:]
		override := machine.BootOverride{
			Mode: "UEFI",
		}

		// Byte 1, bit 7: valid
		// Byte 2, bits 5:2: boot device
		if bootFlags[0]&0x80 != 0 {
			override.Enabled = "Once"
		} else {
			override.Enabled = "Disabled"
		}

		deviceBits := (bootFlags[1] >> 2) & 0x0F
		switch deviceBits {
		case 0x00:
			override.Target = "None"
		case 0x01:
			override.Target = "Pxe"
		case 0x02:
			override.Target = "Hdd"
		case 0x05:
			override.Target = "Cd"
		case 0x06:
			override.Target = "BiosSetup"
		default:
			override.Target = "None"
		}

		// Byte 1, bit 5: UEFI
		if bootFlags[0]&0x20 != 0 {
			override.Mode = "UEFI"
		} else {
			override.Mode = "Legacy"
		}

		if err := m.SetBootOverride(override); err != nil {
			return CompletionCodeInvalidField, nil
		}
		return CompletionCodeOK, nil

	default:
		// Accept but ignore other parameters
		return CompletionCodeOK, nil
	}
}

func handleGetBootOptions(reqData []byte, m MachineInterface) (CompletionCode, []byte) {
	if len(reqData) < 1 {
		return CompletionCodeInvalidField, nil
	}

	paramSelector := reqData[0] & 0x7F

	switch paramSelector {
	case 0: // Set In Progress
		data := []byte{
			0x01,             // Parameter version
			0x80 | 0x00,      // Parameter valid (bit7=1) + selector 0
			0x00,             // Set complete
		}
		return CompletionCodeOK, data

	case 5: // Boot flags
		boot := m.GetBootOverride()
		data := make([]byte, 7)

		// Byte 0: parameter version
		data[0] = 0x01

		// Byte 1: parameter valid (bit7) + parameter selector (bits 6:0)
		data[1] = 0x80 | 0x05

		// Byte 2: boot flags valid (bit7) + UEFI (bit5)
		if boot.Enabled != "Disabled" {
			data[2] = 0x80 // valid
		}
		if boot.Mode == "UEFI" {
			data[2] |= 0x20 // UEFI
		}

		// Byte 3: boot device (bits 5:2)
		var deviceBits byte
		switch boot.Target {
		case "Pxe":
			deviceBits = 0x01
		case "Hdd":
			deviceBits = 0x02
		case "Cd":
			deviceBits = 0x05
		case "BiosSetup":
			deviceBits = 0x06
		default:
			deviceBits = 0x00
		}
		data[3] = deviceBits << 2

		return CompletionCodeOK, data

	default:
		// Return OK with parameter valid=0 for unsupported parameters
		data := []byte{
			0x01,            // Parameter version
			paramSelector,   // Parameter valid=0 (bit7=0) + selector
			0x00,            // Empty data
		}
		return CompletionCodeOK, data
	}
}

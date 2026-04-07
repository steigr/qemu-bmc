package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/machine"
	"github.com/steigr/qemu-bmc/internal/qmp"
)

// mockMachine implements MachineInterface for testing
type mockMachine struct {
	powerState   machine.PowerState
	qmpStatus    qmp.Status
	bootOverride machine.BootOverride
	calls        []string
	lastMedia    string
	resetErr     error
}

func newMockMachine(status qmp.Status) *mockMachine {
	var ps machine.PowerState
	switch status {
	case qmp.StatusRunning:
		ps = machine.PowerOn
	default:
		ps = machine.PowerOff
	}
	return &mockMachine{
		powerState: ps,
		qmpStatus:  status,
		bootOverride: machine.BootOverride{
			Enabled: "Disabled",
			Target:  "None",
			Mode:    "UEFI",
		},
	}
}

func (m *mockMachine) GetPowerState() (machine.PowerState, error) {
	return m.powerState, nil
}

func (m *mockMachine) GetQMPStatus() (qmp.Status, error) {
	return m.qmpStatus, nil
}

func (m *mockMachine) Reset(resetType string) error {
	m.calls = append(m.calls, resetType)
	return m.resetErr
}

func (m *mockMachine) GetBootOverride() machine.BootOverride {
	return m.bootOverride
}

func (m *mockMachine) SetBootOverride(override machine.BootOverride) error {
	// Validate target
	validTargets := map[string]bool{
		"None": true, "Pxe": true, "Hdd": true, "Cd": true, "BiosSetup": true,
	}
	if !validTargets[override.Target] {
		return fmt.Errorf("invalid boot target: %s", override.Target)
	}

	// Validate enabled
	validEnabled := map[string]bool{
		"Disabled": true, "Once": true, "Continuous": true,
	}
	if !validEnabled[override.Enabled] {
		return fmt.Errorf("invalid boot enabled: %s", override.Enabled)
	}

	m.bootOverride = override
	return nil
}

func (m *mockMachine) InsertMedia(image string) error {
	m.lastMedia = image
	m.calls = append(m.calls, "InsertMedia")
	return nil
}

func (m *mockMachine) EjectMedia() error {
	m.lastMedia = ""
	m.calls = append(m.calls, "EjectMedia")
	return nil
}

func (m *mockMachine) Calls() []string {
	return m.calls
}

func (m *mockMachine) LastInsertedMedia() string {
	return m.lastMedia
}

func TestServiceRoot(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var root ServiceRoot
	err := json.Unmarshal(w.Body.Bytes(), &root)
	require.NoError(t, err)

	assert.Equal(t, "#ServiceRoot.v1_5_0.ServiceRoot", root.ODataType)
	assert.Equal(t, "/redfish/v1", root.ODataID)
	assert.Equal(t, "RootService", root.ID)
	assert.Equal(t, "Root Service", root.Name)
	assert.Equal(t, "1.0.0", root.RedfishVersion)
	assert.Equal(t, "/redfish/v1/Systems", root.Systems.ODataID)
	assert.Equal(t, "/redfish/v1/Managers", root.Managers.ODataID)
	assert.Equal(t, "/redfish/v1/Chassis", root.Chassis.ODataID)
}

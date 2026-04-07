package machine

import (
	"errors"
	"testing"
	"time"

	"github.com/steigr/qemu-bmc/internal/qmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockQMPClient implements qmp.Client for testing
type mockQMPClient struct {
	status     qmp.Status
	calls      []string
	connectErr error
	queryErr   error
}

func newMockQMPClient(status qmp.Status) *mockQMPClient {
	return &mockQMPClient{status: status}
}

func (m *mockQMPClient) Connect() error {
	m.calls = append(m.calls, "Connect")
	if m.connectErr != nil {
		return m.connectErr
	}
	return nil
}

func (m *mockQMPClient) QueryStatus() (qmp.Status, error) {
	m.calls = append(m.calls, "QueryStatus")
	if m.queryErr != nil {
		return "", m.queryErr
	}
	return m.status, nil
}

func (m *mockQMPClient) SystemPowerdown() error {
	m.calls = append(m.calls, "SystemPowerdown")
	return nil
}

func (m *mockQMPClient) SystemReset() error {
	m.calls = append(m.calls, "SystemReset")
	return nil
}

func (m *mockQMPClient) SetBootOrder(_ string) error {
	m.calls = append(m.calls, "SetBootOrder")
	return nil
}

func (m *mockQMPClient) Stop() error {
	m.calls = append(m.calls, "Stop")
	m.status = qmp.StatusPaused
	return nil
}

func (m *mockQMPClient) Cont() error {
	m.calls = append(m.calls, "Cont")
	m.status = qmp.StatusRunning
	return nil
}

func (m *mockQMPClient) Quit() error {
	m.calls = append(m.calls, "Quit")
	m.status = qmp.StatusShutdown
	return nil
}

func (m *mockQMPClient) BlockdevChangeMedium(device, filename string) error {
	m.calls = append(m.calls, "BlockdevChangeMedium")
	return nil
}

func (m *mockQMPClient) BlockdevRemoveMedium(device string) error {
	m.calls = append(m.calls, "BlockdevRemoveMedium")
	return nil
}

func (m *mockQMPClient) Close() error {
	return nil
}

func (m *mockQMPClient) Calls() []string {
	return m.calls
}

// mockProcessManager implements ProcessManager for testing
type mockProcessManager struct {
	running    bool
	startCalls []string // boot targets passed to Start
	calls      []string
	exitCh     chan struct{}
}

func newMockProcessManager(running bool) *mockProcessManager {
	ch := make(chan struct{})
	if !running {
		close(ch)
	}
	return &mockProcessManager{
		running: running,
		exitCh:  ch,
	}
}

func (m *mockProcessManager) Start(bootTarget string) error {
	m.calls = append(m.calls, "Start")
	m.startCalls = append(m.startCalls, bootTarget)
	m.running = true
	m.exitCh = make(chan struct{})
	return nil
}

func (m *mockProcessManager) Stop(timeout time.Duration) error {
	m.calls = append(m.calls, "Stop")
	m.running = false
	select {
	case <-m.exitCh:
	default:
		close(m.exitCh)
	}
	return nil
}

func (m *mockProcessManager) Kill() error {
	m.calls = append(m.calls, "Kill")
	m.running = false
	select {
	case <-m.exitCh:
	default:
		close(m.exitCh)
	}
	return nil
}

func (m *mockProcessManager) IsRunning() bool {
	return m.running
}

func (m *mockProcessManager) WaitForExit(timeout time.Duration) error {
	m.calls = append(m.calls, "WaitForExit")
	select {
	case <-m.exitCh:
		return nil
	case <-time.After(timeout):
		return errors.New("timeout")
	}
}

// --- Legacy mode tests (existing) ---

func TestGetPowerState_Running(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state)
}

func TestGetPowerState_Shutdown(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusShutdown)
	m := New(mock)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
}

func TestReset_ForceOff(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("ForceOff")
	require.NoError(t, err)
	assert.Equal(t, "SystemReset", mock.Calls()[0])
	assert.Equal(t, "Stop", mock.Calls()[len(mock.Calls())-1])
}

func TestReset_GracefulShutdown(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("GracefulShutdown")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "SystemPowerdown")
}

func TestReset_ForceRestart(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("ForceRestart")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "SystemReset")
}

func TestReset_ForceRestart_AppliesCdBootOnce(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Cd", Mode: "UEFI"})
	require.NoError(t, err)

	err = m.Reset("ForceRestart")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "SetBootOrder")
	assert.Contains(t, mock.Calls(), "SystemReset")

	boot := m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)
}

func TestReset_GracefulRestart(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("GracefulRestart")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "SystemReset")
}

func TestReset_InvalidType(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("BadType")
	assert.Error(t, err)
}

func TestBootOverride(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	// Default should be Disabled
	boot := m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)

	// Set PXE Once
	err := m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"})
	require.NoError(t, err)

	boot = m.GetBootOverride()
	assert.Equal(t, "Once", boot.Enabled)
	assert.Equal(t, "Pxe", boot.Target)

	// Consume boot once
	m.ConsumeBootOnce()
	boot = m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)
}

func TestBootOverride_InvalidTarget(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Invalid", Mode: "UEFI"})
	assert.Error(t, err)
}

func TestInsertMedia(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.InsertMedia("http://example.com/boot.iso")
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "BlockdevChangeMedium")
}

func TestEjectMedia(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.EjectMedia()
	require.NoError(t, err)
	assert.Contains(t, mock.Calls(), "BlockdevRemoveMedium")
}

// --- Process mode tests ---

func TestProcessMode_GetPowerState_ProcessNotRunning(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusShutdown)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
	// Should not call QMP when process is not running
	assert.NotContains(t, qmpMock.Calls(), "QueryStatus")
}

func TestProcessMode_GetPowerState_ProcessRunning(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state)
}

func TestProcessMode_GetPowerState_ProcessRunning_QMPError(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.queryErr = errors.New("QMP not ready")
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOn, state) // Process running = PowerOn even if QMP fails
}

func TestProcessMode_GetPowerState_GuestShutdown(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusShutdown)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
	assert.Contains(t, pm.calls, "Stop") // Process should be stopped
}

func TestProcessMode_GetQMPStatus_ProcessNotRunning(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusShutdown)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	status, err := m.GetQMPStatus()
	require.NoError(t, err)
	assert.Equal(t, qmp.StatusShutdown, status)
}

func TestProcessMode_GetQMPStatus_ProcessRunning_QMPError(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.queryErr = errors.New("QMP not ready")
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	status, err := m.GetQMPStatus()
	require.NoError(t, err)
	assert.Equal(t, qmp.StatusRunning, status) // synthetic running status
}

func TestProcessMode_Reset_On_StartsProcess(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	err := m.Reset("On")
	require.NoError(t, err)
	assert.Contains(t, pm.calls, "Start")
	assert.Contains(t, qmpMock.Calls(), "Connect")
}

func TestProcessMode_Reset_On_AlreadyRunning_Noop(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	err := m.Reset("On")
	require.NoError(t, err)
	assert.NotContains(t, pm.calls, "Start")
}

func TestProcessMode_Reset_On_WithBootOverride(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"})
	err := m.Reset("On")
	require.NoError(t, err)

	require.Len(t, pm.startCalls, 1)
	assert.Equal(t, "Pxe", pm.startCalls[0])
}

func TestProcessMode_Reset_On_ConsumesBootOnce(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)

	m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"})
	err := m.Reset("On")
	require.NoError(t, err)

	boot := m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)
}

func TestProcessMode_Reset_ForceOff_ResetsAndStops(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	err := m.Reset("ForceOff")
	require.NoError(t, err)
	assert.Equal(t, "SystemReset", qmpMock.Calls()[0])
	assert.Equal(t, "Stop", qmpMock.Calls()[len(qmpMock.Calls())-1])
	assert.NotContains(t, pm.calls, "WaitForExit")
	assert.NotContains(t, pm.calls, "Stop")
}

func TestProcessMode_Reset_ForceRestart_SystemReset(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	err := m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Cd", Mode: "UEFI"})
	require.NoError(t, err)

	err = m.Reset("ForceRestart")
	require.NoError(t, err)
	assert.Contains(t, pm.calls, "Stop")
	assert.Contains(t, pm.calls, "Start")
	assert.Contains(t, qmpMock.Calls(), "Connect")
	assert.NotContains(t, qmpMock.Calls(), "SystemReset")

	boot := m.GetBootOverride()
	assert.Equal(t, "Disabled", boot.Enabled)
	assert.Equal(t, "None", boot.Target)
}

func TestProcessMode_Reset_ForceRestart_WithoutBootOverride_UsesSystemReset(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	err := m.Reset("ForceRestart")
	require.NoError(t, err)
	assert.Contains(t, qmpMock.Calls(), "SystemReset")
	assert.NotContains(t, pm.calls, "Stop")
	assert.NotContains(t, pm.calls, "Start")
}

func TestProcessMode_Reset_GracefulShutdown(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	// Close exitCh so WaitForExit returns immediately
	pm.running = false
	close(pm.exitCh)

	err := m.Reset("GracefulShutdown")
	require.NoError(t, err)
	assert.Contains(t, qmpMock.Calls(), "SystemPowerdown")
	assert.Contains(t, pm.calls, "WaitForExit")
}

func TestLegacyMode_Reset_ForceOff_ResetsAndStopsQMP(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)

	err := m.Reset("ForceOff")
	require.NoError(t, err)
	assert.Equal(t, "SystemReset", mock.Calls()[0])
	assert.Equal(t, "Stop", mock.Calls()[len(mock.Calls())-1])
}

func TestProcessMode_GetPowerState_Paused_IsOff(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusPaused)
	pm := newMockProcessManager(true)
	m := NewWithProcess(qmpMock, pm)

	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)
}

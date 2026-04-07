package qmp

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_QueryStatus_PowerOn(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	mockQMP.SetStatus(StatusRunning)
	defer mockQMP.Close()

	// Give the server a moment to start listening
	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)
}

func TestClient_QueryStatus_PowerOff(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	mockQMP.SetStatus(StatusShutdown)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusShutdown, status)
}

func TestClient_SystemPowerdown(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.SystemPowerdown()
	require.NoError(t, err)
	assert.Equal(t, "system_powerdown", mockQMP.LastCommand())
}

func TestClient_SystemReset(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.SystemReset()
	require.NoError(t, err)
	assert.Equal(t, "system_reset", mockQMP.LastCommand())
}

func TestClient_SetBootOrder(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.SetBootOrder("d")
	require.NoError(t, err)
	assert.Contains(t, []string{"set_boot_device", "human-monitor-command"}, mockQMP.LastCommand())
}

func TestClient_Quit(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.Quit()
	require.NoError(t, err)
	assert.Equal(t, "quit", mockQMP.LastCommand())
}

func TestClient_BlockdevChangeMedium(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.BlockdevChangeMedium("ide0-cd0", "/path/to/image.iso")
	require.NoError(t, err)
	assert.Equal(t, "blockdev-change-medium", mockQMP.LastCommand())
}

func TestClient_BlockdevRemoveMedium(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)
	defer client.Close()

	err = client.BlockdevRemoveMedium("ide0-cd0")
	require.NoError(t, err)
	assert.Equal(t, "blockdev-remove-medium", mockQMP.LastCommand())
}

func TestNewDisconnectedClient_NotConnected(t *testing.T) {
	client := NewDisconnectedClient("/tmp/nonexistent.sock")
	defer client.Close()

	_, err := client.QueryStatus()
	assert.ErrorIs(t, err, ErrNotConnected)
}

func TestDisconnectedClient_Connect(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	mockQMP.SetStatus(StatusRunning)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client := NewDisconnectedClient(socketPath)
	defer client.Close()

	// Should fail before connect
	_, err := client.QueryStatus()
	assert.ErrorIs(t, err, ErrNotConnected)

	// Connect
	err = client.Connect()
	require.NoError(t, err)

	// Should work after connect
	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)
}

func TestClient_Close_ThenConnect(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "qmp.sock")
	mockQMP := newMockQMPServer(t, socketPath)
	mockQMP.SetStatus(StatusRunning)
	defer mockQMP.Close()

	time.Sleep(50 * time.Millisecond)

	client, err := NewClient(socketPath)
	require.NoError(t, err)

	// Verify connected
	status, err := client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)

	// Close
	err = client.Close()
	require.NoError(t, err)

	// Reconnect
	err = client.Connect()
	require.NoError(t, err)

	// Should work again
	status, err = client.QueryStatus()
	require.NoError(t, err)
	assert.Equal(t, StatusRunning, status)

	client.Close()
}

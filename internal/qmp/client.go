package qmp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// ErrNotConnected is returned when a command is issued on a disconnected client.
var ErrNotConnected = errors.New("QMP client not connected")

// qmpClient implements the Client interface
type qmpClient struct {
	socketPath string
	conn       net.Conn
	scanner    *bufio.Scanner
	connected  bool
	mu         sync.Mutex
}

// NewClient creates a new QMP client connected to the given UNIX socket
func NewClient(socketPath string) (Client, error) {
	c := NewDisconnectedClient(socketPath)
	if err := c.Connect(); err != nil {
		return nil, err
	}
	return c, nil
}

// NewDisconnectedClient creates a QMP client that is not yet connected.
// Call Connect() to establish the connection.
func NewDisconnectedClient(socketPath string) Client {
	return &qmpClient{
		socketPath: socketPath,
	}
}

// Connect establishes (or re-establishes) the QMP connection.
func (c *qmpClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Close existing connection if any
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
		c.scanner = nil
		c.connected = false
	}

	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("connecting to QMP socket: %w", err)
	}

	c.conn = conn
	c.scanner = bufio.NewScanner(conn)

	// Read QMP greeting
	if !c.scanner.Scan() {
		conn.Close()
		c.conn = nil
		c.scanner = nil
		return fmt.Errorf("reading QMP greeting: connection closed")
	}

	var greeting qmpGreeting
	if err := json.Unmarshal(c.scanner.Bytes(), &greeting); err != nil {
		conn.Close()
		c.conn = nil
		c.scanner = nil
		return fmt.Errorf("parsing QMP greeting: %w", err)
	}

	// Send qmp_capabilities
	if err := c.executeLocked("qmp_capabilities", nil); err != nil {
		conn.Close()
		c.conn = nil
		c.scanner = nil
		return fmt.Errorf("QMP capabilities negotiation: %w", err)
	}

	c.connected = true
	return nil
}

func (c *qmpClient) checkConnected() error {
	if !c.connected {
		return ErrNotConnected
	}
	return nil
}

// executeLocked sends a command and reads the response. Caller must hold c.mu.
func (c *qmpClient) executeLocked(command string, arguments interface{}) error {
	cmd := qmpCommand{
		Execute:   command,
		Arguments: arguments,
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("marshaling command: %w", err)
	}

	data = append(data, '\n')
	if _, err := c.conn.Write(data); err != nil {
		return fmt.Errorf("writing command: %w", err)
	}

	// Read response, skipping async events
	for {
		if !c.scanner.Scan() {
			return fmt.Errorf("reading response: connection closed")
		}

		var resp qmpResponse
		if err := json.Unmarshal(c.scanner.Bytes(), &resp); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		// Skip async events (they have "event" field, not "return"/"error")
		if resp.Event != "" {
			continue
		}

		if resp.Error != nil {
			return fmt.Errorf("QMP error: %s: %s", resp.Error.Class, resp.Error.Desc)
		}

		return nil
	}
}

func (c *qmpClient) execute(command string, arguments interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkConnected(); err != nil {
		return err
	}

	return c.executeLocked(command, arguments)
}

func (c *qmpClient) executeWithResponse(command string, arguments interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.checkConnected(); err != nil {
		return nil, err
	}

	cmd := qmpCommand{
		Execute:   command,
		Arguments: arguments,
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshaling command: %w", err)
	}

	data = append(data, '\n')
	if _, err := c.conn.Write(data); err != nil {
		return nil, fmt.Errorf("writing command: %w", err)
	}

	// Read response, skipping async events
	for {
		if !c.scanner.Scan() {
			return nil, fmt.Errorf("reading response: connection closed")
		}

		var resp qmpResponse
		rawBytes := make([]byte, len(c.scanner.Bytes()))
		copy(rawBytes, c.scanner.Bytes())

		if err := json.Unmarshal(rawBytes, &resp); err != nil {
			return nil, fmt.Errorf("parsing response: %w", err)
		}

		// Skip async events
		if resp.Event != "" {
			continue
		}

		return json.RawMessage(rawBytes), nil
	}
}

func (c *qmpClient) QueryStatus() (Status, error) {
	raw, err := c.executeWithResponse("query-status", nil)
	if err != nil {
		return "", err
	}

	var resp qmpStatusResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parsing status response: %w", err)
	}

	return Status(resp.Return.Status), nil
}

func (c *qmpClient) SystemPowerdown() error {
	return c.execute("system_powerdown", nil)
}

func (c *qmpClient) SystemReset() error {
	return c.execute("system_reset", nil)
}

func (c *qmpClient) SetBootOrder(order string) error {
	if err := c.execute("set_boot_device", setBootDeviceArgs{BootDevice: order}); err == nil {
		return nil
	}

	candidates := []string{
		fmt.Sprintf("boot_set order=%s", order),
		fmt.Sprintf("boot_set %s", order),
		fmt.Sprintf("set_boot_device %s", order),
	}

	errorsSeen := make([]string, 0, len(candidates))
	for _, cmd := range candidates {
		out, err := c.humanMonitorCommand(cmd)
		if err != nil {
			errorsSeen = append(errorsSeen, fmt.Sprintf("%s: %v", cmd, err))
			continue
		}
		if monitorOutputIndicatesError(out) {
			errorsSeen = append(errorsSeen, fmt.Sprintf("%s: %s", cmd, strings.TrimSpace(out)))
			continue
		}
		return nil
	}

	if len(errorsSeen) == 0 {
		return fmt.Errorf("failed to set boot order %q", order)
	}
	return fmt.Errorf("failed to set boot order %q: %s", order, strings.Join(errorsSeen, "; "))
}

func (c *qmpClient) humanMonitorCommand(commandLine string) (string, error) {
	raw, err := c.executeWithResponse("human-monitor-command", humanMonitorCommandArgs{CommandLine: commandLine})
	if err != nil {
		return "", err
	}

	var resp qmpResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("QMP error: %s: %s", resp.Error.Class, resp.Error.Desc)
	}

	if out, ok := resp.Return.(string); ok {
		return out, nil
	}
	return "", nil
}

func monitorOutputIndicatesError(out string) bool {
	if out == "" {
		return false
	}
	text := strings.ToLower(out)
	indicators := []string{"unknown command", "invalid", "error", "not found", "not supported", "failed"}
	for _, s := range indicators {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

func (c *qmpClient) Stop() error {
	return c.execute("stop", nil)
}

func (c *qmpClient) Cont() error {
	return c.execute("cont", nil)
}

func (c *qmpClient) Quit() error {
	return c.execute("quit", nil)
}

func (c *qmpClient) BlockdevChangeMedium(device, filename string) error {
	return c.execute("blockdev-change-medium", blockdevChangeMediumArgs{
		Device:   device,
		Filename: filename,
	})
}

func (c *qmpClient) BlockdevRemoveMedium(device string) error {
	return c.execute("blockdev-remove-medium", blockdevRemoveMediumArgs{
		Device: device,
	})
}

func (c *qmpClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.connected = false
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

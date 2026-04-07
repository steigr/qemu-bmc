package qmp

// Status represents QEMU VM running status
type Status string

const (
	StatusRunning  Status = "running"
	StatusShutdown Status = "shutdown"
	StatusPaused   Status = "paused"
)

// Client is the interface for QMP communication
type Client interface {
	Connect() error
	QueryStatus() (Status, error)
	SystemPowerdown() error
	SystemReset() error
	SetBootOrder(order string) error
	Stop() error
	Cont() error
	Quit() error
	BlockdevChangeMedium(device, filename string) error
	BlockdevRemoveMedium(device string) error
	Close() error
}

// QMP protocol message types
type qmpGreeting struct {
	QMP struct {
		Version struct {
			QEMU struct {
				Micro int `json:"micro"`
				Minor int `json:"minor"`
				Major int `json:"major"`
			} `json:"qemu"`
		} `json:"version"`
		Capabilities []interface{} `json:"capabilities"`
	} `json:"QMP"`
}

type qmpCommand struct {
	Execute   string      `json:"execute"`
	Arguments interface{} `json:"arguments,omitempty"`
}

type qmpResponse struct {
	Return interface{} `json:"return,omitempty"`
	Error  *qmpError   `json:"error,omitempty"`
	Event  string      `json:"event,omitempty"`
}

type qmpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

type qmpStatusResponse struct {
	Return struct {
		Running bool   `json:"running"`
		Status  string `json:"status"`
	} `json:"return"`
}

type blockdevChangeMediumArgs struct {
	Device   string `json:"device"`
	Filename string `json:"filename"`
}

type blockdevRemoveMediumArgs struct {
	Device string `json:"device"`
}

type humanMonitorCommandArgs struct {
	CommandLine string `json:"command-line"`
}

type setBootDeviceArgs struct {
	BootDevice string `json:"bootdevice"`
}


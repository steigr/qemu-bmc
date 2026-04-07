package ipmi

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	"github.com/steigr/qemu-bmc/internal/bmc"
)

// VMServer is a TCP server that speaks the OpenIPMI VM wire protocol.
// It accepts connections from QEMU's ipmi-bmc-extern chardev and routes
// IPMI commands to the shared handleIPMICommand dispatcher.
type VMServer struct {
	machine  MachineInterface
	bmcState *bmc.State
	listener net.Listener
	mu       sync.Mutex
	vmCaps   uint8 // capabilities reported by QEMU
}

// NewVMServer creates a new VMServer.
func NewVMServer(machine MachineInterface, state *bmc.State) *VMServer {
	return &VMServer{
		machine:  machine,
		bmcState: state,
	}
}

// ListenAndServe starts a TCP listener and accepts connections.
// Each connection is handled in a separate goroutine.
func (vs *VMServer) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("VM server listen on %s: %w", addr, err)
	}
	vs.mu.Lock()
	vs.listener = ln
	vs.mu.Unlock()

	log.Printf("VM server listening on %s", addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Check if listener was closed
			vs.mu.Lock()
			closed := vs.listener == nil
			vs.mu.Unlock()
			if closed {
				return nil
			}
			return fmt.Errorf("VM server accept: %w", err)
		}
		log.Printf("VM server: new connection from %s", conn.RemoteAddr())
		go func() {
			if err := vs.HandleConnection(conn); err != nil {
				log.Printf("VM server: connection error: %v", err)
			}
		}()
	}
}

// HandleConnection handles a single VM protocol connection.
// It reads framed messages, dispatches control commands and IPMI messages,
// and returns nil on EOF or an error on failure.
func (vs *VMServer) HandleConnection(conn net.Conn) error {
	defer conn.Close()

	reader := &vmReader{conn: conn}
	for {
		terminator, data, err := reader.ReadMessage()
		if err != nil {
			if err == io.EOF {
				log.Printf("VM server: connection closed")
				return nil
			}
			return fmt.Errorf("VM server read: %w", err)
		}

		switch terminator {
		case VMCmdChar:
			vs.handleControlCommand(data, conn)
		case VMMsgChar:
			vs.handleIPMIMsg(data, conn)
		}
	}
}

// handleControlCommand processes a VM control command.
func (vs *VMServer) handleControlCommand(data []byte, conn net.Conn) {
	cmd, rest, err := vmParseControlCommand(data)
	if err != nil {
		log.Printf("VM server: invalid control command: %v", err)
		return
	}

	switch cmd {
	case VMCmdVersion:
		ver := uint8(0)
		if len(rest) > 0 {
			ver = rest[0]
		}
		log.Printf("VM server: peer version %d", ver)

	case VMCmdCapabilities:
		caps := uint8(0)
		if len(rest) > 0 {
			caps = rest[0]
		}
		vs.mu.Lock()
		vs.vmCaps = caps
		vs.mu.Unlock()
		log.Printf("VM server: peer capabilities 0x%02x", caps)

		// Send NOATTN response
		vs.sendControlCommand(conn, VMCmdNoAttn)

	default:
		log.Printf("VM server: unknown control command 0x%02x", cmd)
	}
}

// handleIPMIMsg processes an IPMI message received over the VM protocol.
func (vs *VMServer) handleIPMIMsg(data []byte, conn net.Conn) {
	req, err := vmParseIPMIRequest(data)
	if err != nil {
		log.Printf("VM server: invalid IPMI request: %v", err)
		return
	}

	// Convert VMIPMIRequest to IPMIMessage for the shared handler
	msg := &IPMIMessage{
		TargetLun: (req.NetFn << 2) | (req.LUN & 0x03),
		Command:   req.Cmd,
		Data:      req.Data,
	}

	// Route to the shared IPMI command handler
	code, respData := handleIPMICommand(msg, vs.machine, vs.bmcState)

	// Build VM protocol response
	respNetFn := req.NetFn | 0x01
	response := vmBuildIPMIResponse(req.Seq, respNetFn, req.LUN, req.Cmd, code, respData)

	// Escape and frame
	escaped := vmEscapeBytes(response)
	escaped = append(escaped, VMMsgChar)

	if _, err := conn.Write(escaped); err != nil {
		log.Printf("VM server: write IPMI response error: %v", err)
	}
}

// sendControlCommand sends a control command to the peer.
func (vs *VMServer) sendControlCommand(conn net.Conn, cmd uint8, data ...byte) {
	raw := vmBuildControlCommand(cmd, data...)
	escaped := vmEscapeBytes(raw)
	escaped = append(escaped, VMCmdChar)

	if _, err := conn.Write(escaped); err != nil {
		log.Printf("VM server: write control command error: %v", err)
	}
}

// Close closes the listener, stopping the server from accepting new connections.
func (vs *VMServer) Close() error {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if vs.listener != nil {
		err := vs.listener.Close()
		vs.listener = nil
		return err
	}
	return nil
}

// vmReader reads framed messages from a VM protocol stream.
type vmReader struct {
	conn net.Conn
}

// ReadMessage reads bytes one at a time until a VMMsgChar or VMCmdChar terminator
// is seen. It unescapes the accumulated bytes and returns the terminator type,
// unescaped data, and any error.
func (r *vmReader) ReadMessage() (byte, []byte, error) {
	var buf []byte
	b := make([]byte, 1)

	for {
		n, err := r.conn.Read(b)
		if err != nil {
			return 0, nil, err
		}
		if n == 0 {
			continue
		}

		if b[0] == VMMsgChar || b[0] == VMCmdChar {
			terminator := b[0]
			unescaped, err := vmUnescapeBytes(buf)
			if err != nil {
				return 0, nil, fmt.Errorf("VM reader unescape: %w", err)
			}
			return terminator, unescaped, nil
		}

		buf = append(buf, b[0])
	}
}

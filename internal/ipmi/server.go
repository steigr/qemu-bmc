package ipmi

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"

	"github.com/steigr/qemu-bmc/internal/bmc"
)

// Server is the IPMI UDP server
type Server struct {
	machine    MachineInterface
	bmcState   *bmc.State
	sessionMgr *SessionManager
	user       string
	pass       string
	conn       net.PacketConn
}

// NewServer creates a new IPMI server
func NewServer(m MachineInterface, state *bmc.State, user, pass string) *Server {
	return &Server{
		machine:    m,
		bmcState:   state,
		sessionMgr: NewSessionManager(),
		user:       user,
		pass:       pass,
	}
}

// ListenAndServe starts the IPMI UDP server
func (s *Server) ListenAndServe(addr string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	s.conn = conn
	log.Printf("IPMI server listening on %s", addr)
	return s.serve()
}

// Serve starts serving on an existing connection (useful for testing)
func (s *Server) Serve(conn net.PacketConn) error {
	s.conn = conn
	return s.serve()
}

func (s *Server) serve() error {
	buf := make([]byte, 1024)
	for {
		n, addr, err := s.conn.ReadFrom(buf)
		if err != nil {
			return err
		}

		// Make a copy of the data
		data := make([]byte, n)
		copy(data, buf[:n])

		resp, err := s.HandleMessage(data)
		if err != nil {
			log.Printf("IPMI error: %v", err)
			continue
		}

		if resp != nil {
			if _, err := s.conn.WriteTo(resp, addr); err != nil {
				log.Printf("IPMI write error: %v", err)
			}
		}
	}
}

// HandleMessage processes a single IPMI/RMCP message and returns a response
func (s *Server) HandleMessage(data []byte) ([]byte, error) {
	header, payload, err := ParseRMCPMessage(data)
	if err != nil {
		return nil, err
	}

	if header.Class == RMCPClassASF {
		return handleASFPing(payload)
	}

	if header.Class != RMCPClassIPMI {
		return nil, fmt.Errorf("unsupported RMCP class: 0x%02x", header.Class)
	}

	// Check if this is RMCP+ (auth type 0x06 at first byte of payload)
	if len(payload) > 0 && payload[0] == AuthTypeRMCPPlus {
		resp, err := HandleRMCPPlusMessage(payload, s.sessionMgr, s.user, s.pass, s.machine, s.bmcState)
		if err != nil {
			return nil, err
		}
		return SerializeRMCPMessage(RMCPClassIPMI, resp), nil
	}

	// IPMI 1.5 message
	session, msg, err := ParseIPMI15Message(payload)
	if err != nil {
		return nil, err
	}

	if msg == nil {
		return nil, fmt.Errorf("no IPMI message parsed")
	}

	code, respData := handleIPMICommand(msg, s.machine, s.bmcState)

	respPayload := SerializeIPMIResponse(session, msg.GetNetFn()|0x01, msg.Command, code, respData, msg.SourceLun, s.pass)
	return SerializeRMCPMessage(RMCPClassIPMI, respPayload), nil
}

// handleASFPing responds to ASF Presence Ping with a Pong
func handleASFPing(payload []byte) ([]byte, error) {
	// ASF message header: 4-byte IANA + 1-byte type + 1-byte tag + 1-byte reserved + 1-byte length
	if len(payload) < 8 {
		return nil, fmt.Errorf("ASF message too short")
	}

	msgType := payload[4]
	msgTag := payload[5]

	if msgType != 0x80 { // Only handle Presence Ping
		return nil, fmt.Errorf("unsupported ASF message type: 0x%02x", msgType)
	}

	// Build ASF Presence Pong
	resp := make([]byte, 28) // 4 RMCP + 8 ASF header + 16 pong data

	// RMCP header
	resp[0] = RMCPVersion1
	resp[1] = 0x00
	resp[2] = 0xFF
	resp[3] = RMCPClassASF

	// ASF header
	binary.BigEndian.PutUint32(resp[4:8], 0x000011BE) // IANA Enterprise Number for ASF
	resp[8] = 0x40                                      // Message Type: Presence Pong
	resp[9] = msgTag                                     // Echo back message tag
	resp[10] = 0x00                                      // Reserved
	resp[11] = 0x10                                      // Data Length: 16 bytes

	// Pong data
	binary.BigEndian.PutUint32(resp[12:16], 0x000011BE) // IANA Enterprise Number
	// OEM-defined: 4 bytes zeros (resp[16:20] already zero)
	resp[20] = 0x81 // Supported Entities: IPMI supported (bit 7) + ASF 1.0 (bit 0)
	resp[21] = 0x00 // Supported Interactions
	// Reserved: 6 bytes zeros (resp[22:28] already zero)

	return resp, nil
}

// Close stops the server
func (s *Server) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

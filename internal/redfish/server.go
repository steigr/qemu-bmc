package redfish

import (
	"net/http"
	"sync"

	"github.com/gorilla/mux"
	"github.com/steigr/qemu-bmc/internal/machine"
	"github.com/steigr/qemu-bmc/internal/novnc"
	"github.com/steigr/qemu-bmc/internal/qmp"
)

// MachineInterface defines what the Redfish server needs from the machine layer
type MachineInterface interface {
	GetPowerState() (machine.PowerState, error)
	GetQMPStatus() (qmp.Status, error)
	Reset(resetType string) error
	GetBootOverride() machine.BootOverride
	SetBootOverride(override machine.BootOverride) error
	InsertMedia(image string) error
	EjectMedia() error
}

// Server is the Redfish HTTP server
type Server struct {
	router            *mux.Router
	machine           MachineInterface
	user              string
	pass              string
	currentMedia      string
	originMedia       string
	novncHandler      *novnc.Handler
	mediaProxyClient  *http.Client
	mediaProxySources map[string]*mediaProxySource
	mediaProxyMu      sync.Mutex
}

// NewServer creates a new Redfish server
func NewServer(m MachineInterface, user, pass, vncAddr string) *Server {
	s := &Server{
		router:            mux.NewRouter(),
		machine:           m,
		user:              user,
		pass:              pass,
		novncHandler:      novnc.NewHandler(vncAddr),
		mediaProxyClient:  newMediaProxyHTTPClient(),
		mediaProxySources: make(map[string]*mediaProxySource),
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// Apply middleware
	s.router.Use(s.trailingSlashMiddleware)
	if s.user != "" && s.pass != "" {
		s.router.Use(s.basicAuthMiddleware)
	}

	// Service Root
	s.router.HandleFunc("/redfish/v1", s.handleServiceRoot).Methods("GET")
	s.router.HandleFunc("/redfish/v1/", s.handleServiceRoot).Methods("GET")

	// Systems
	s.router.HandleFunc("/redfish/v1/Systems", s.handleSystemCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/", s.handleSystemCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}", s.handleGetSystem).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/", s.handleGetSystem).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Systems/{id}", s.handlePatchSystem).Methods("PATCH")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/", s.handlePatchSystem).Methods("PATCH")

	// Actions
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Actions/ComputerSystem.Reset", s.handleResetAction).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Systems/{id}/Actions/ComputerSystem.Reset/", s.handleResetAction).Methods("POST")

	// Managers
	s.router.HandleFunc("/redfish/v1/Managers", s.handleManagerCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/", s.handleManagerCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}", s.handleGetManager).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/", s.handleGetManager).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia", s.handleVirtualMediaCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/", s.handleVirtualMediaCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}", s.handleGetVirtualMedia).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/", s.handleGetVirtualMedia).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Actions/VirtualMedia.InsertMedia", s.handleInsertMedia).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Actions/VirtualMedia.InsertMedia/", s.handleInsertMedia).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Actions/VirtualMedia.EjectMedia", s.handleEjectMedia).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Actions/VirtualMedia.EjectMedia/", s.handleEjectMedia).Methods("POST")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Proxy", s.handleVirtualMediaProxy).Methods("GET", "HEAD")
	s.router.HandleFunc("/redfish/v1/Managers/{id}/VirtualMedia/{vmid}/Proxy/", s.handleVirtualMediaProxy).Methods("GET", "HEAD")

	// Chassis
	s.router.HandleFunc("/redfish/v1/Chassis", s.handleChassisCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Chassis/", s.handleChassisCollection).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Chassis/{id}", s.handleGetChassis).Methods("GET")
	s.router.HandleFunc("/redfish/v1/Chassis/{id}/", s.handleGetChassis).Methods("GET")

	// noVNC: redirect /novnc/ → /novnc/vnc.html, serve static files, and WebSocket proxy
	s.router.HandleFunc("/novnc/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/novnc/vnc.html", http.StatusFound)
	}).Methods("GET")
	s.router.PathPrefix("/novnc/").Handler(
		http.StripPrefix("/novnc/", s.novncHandler.ServeFiles()),
	)
	s.router.HandleFunc("/websockify", s.novncHandler.ServeWebSocket)
}

// ServeHTTP implements the http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

package redfish

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"syscall"
)

func (s *Server) handleManagerCollection(w http.ResponseWriter, r *http.Request) {
	col := ManagerCollection{
		ODataType:    "#ManagerCollection.ManagerCollection",
		ODataID:      "/redfish/v1/Managers",
		Name:         "Manager Collection",
		MembersCount: 1,
		Members:      []ODataID{{ODataID: "/redfish/v1/Managers/1"}},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(col)
}

func (s *Server) handleGetManager(w http.ResponseWriter, r *http.Request) {
	mgr := Manager{
		ODataType:    "#Manager.v1_3_0.Manager",
		ODataID:      "/redfish/v1/Managers/1",
		ODataContext: "/redfish/v1/$metadata#Manager.Manager",
		ID:           "1",
		Name:         "QEMU BMC",
		ManagerType:  "BMC",
		VirtualMedia: ODataID{ODataID: "/redfish/v1/Managers/1/VirtualMedia"},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(mgr)
}

func (s *Server) handleVirtualMediaCollection(w http.ResponseWriter, r *http.Request) {
	col := VirtualMediaCollection{
		ODataType:    "#VirtualMediaCollection.VirtualMediaCollection",
		ODataID:      "/redfish/v1/Managers/1/VirtualMedia",
		Name:         "Virtual Media Collection",
		MembersCount: 1,
		Members:      []ODataID{{ODataID: "/redfish/v1/Managers/1/VirtualMedia/CD1"}},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(col)
}

func (s *Server) handleGetVirtualMedia(w http.ResponseWriter, r *http.Request) {
	vm := VirtualMedia{
		ODataType:    "#VirtualMedia.v1_2_0.VirtualMedia",
		ODataID:      "/redfish/v1/Managers/1/VirtualMedia/CD1",
		ODataContext: "/redfish/v1/$metadata#VirtualMedia.VirtualMedia",
		ID:           "CD1",
		Name:         "Virtual CD",
		MediaTypes:   []string{"CD", "DVD"},
		Image:        s.originMedia,
		Inserted:     s.originMedia != "",
		ConnectedVia: func() string {
			if s.originMedia != "" {
				return "URI"
			}
			return "NotConnected"
		}(),
		Actions: VirtualMediaActions{
			InsertMedia: VirtualMediaAction{Target: "/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia"},
			EjectMedia:  VirtualMediaAction{Target: "/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia"},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vm)
}

func (s *Server) handleInsertMedia(w http.ResponseWriter, r *http.Request) {
	wasEmpty := s.originMedia == ""
	boot := s.machine.GetBootOverride()
	restartOnInsert := wasEmpty && boot.Enabled == "Once" && boot.Target == "Cd"

	var req InsertMediaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "Invalid request body")
		return
	}

	if req.Image == "" {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "Image URL is required")
		return
	}

	proxyURL := s.buildProxyURL(r, req.Image)
	source := s.getProxySource(req.Image)
	if err := source.ensureReady(s.mediaProxyClient); err != nil {
		writeError(w, http.StatusBadGateway, "InternalError", "failed to prepare virtual media proxy")
		return
	}

	// Track media state in BMC. QMP insertion is best-effort.
	if err := s.machine.InsertMedia(proxyURL); err != nil {
		log.Printf("VirtualMedia: QMP insert failed (non-fatal, proxyURL=%s): %v", proxyURL, err)
	}
	if restartOnInsert {
		if !signalGovernanceReset() {
			if err := s.machine.Reset("ForceRestart"); err != nil {
				log.Printf("VirtualMedia: ForceRestart after first insert with one-time CD boot failed: %v", err)
			}
		}
	}

	s.currentMedia = proxyURL
	s.originMedia = req.Image
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func signalGovernanceReset() bool {
	pidStr := os.Getenv("QEMU_BMC_GOVERNANCE_PID")
	if pidStr == "" {
		return false
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 1 {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.SIGUSR2); err != nil {
		return false
	}
	log.Printf("VirtualMedia: requested governance reset via SIGUSR2 (pid=%d)", pid)
	return true
}

func (s *Server) handleEjectMedia(w http.ResponseWriter, r *http.Request) {
	if err := s.machine.EjectMedia(); err != nil {
		log.Printf("VirtualMedia: QMP eject failed (non-fatal): %v", err)
	}

	s.currentMedia = ""
	s.originMedia = ""
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

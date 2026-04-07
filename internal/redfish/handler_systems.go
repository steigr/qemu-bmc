package redfish

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/steigr/qemu-bmc/internal/machine"
	"github.com/steigr/qemu-bmc/internal/qmp"
)

func (s *Server) handleSystemCollection(w http.ResponseWriter, r *http.Request) {
	collection := SystemCollection{
		ODataType:    "#ComputerSystemCollection.ComputerSystemCollection",
		ODataID:      "/redfish/v1/Systems",
		Name:         "Computer System Collection",
		MembersCount: 1,
		Members: []ODataID{
			{ODataID: "/redfish/v1/Systems/1"},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(collection)
}

func (s *Server) handleGetSystem(w http.ResponseWriter, r *http.Request) {
	status, err := s.machine.GetQMPStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", "failed to get system status")
		return
	}

	powerState := mapQMPStatusToRedfish(status)
	boot := s.machine.GetBootOverride()

	etag := generateETag(powerState, boot)

	system := ComputerSystem{
		ODataType: "#ComputerSystem.v1_5_0.ComputerSystem",
		ODataID:   "/redfish/v1/Systems/1",
		ODataEtag: etag,
		ID:        "1",
		Name:      "QEMU Virtual Machine",
		PowerState: powerState,
		Boot: BootSource{
			BootSourceOverrideEnabled: boot.Enabled,
			BootSourceOverrideTarget:  boot.Target,
			BootSourceOverrideMode:    boot.Mode,
			AllowableValues:           []string{"None", "Pxe", "Hdd", "Cd", "BiosSetup"},
		},
		Actions: ComputerSystemActions{
			Reset: ResetAction{
				Target:          "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset",
				AllowableValues: []string{"On", "ForceOff", "GracefulShutdown", "ForceRestart", "GracefulRestart"},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	json.NewEncoder(w).Encode(system)
}

func (s *Server) handlePatchSystem(w http.ResponseWriter, r *http.Request) {
	// Check If-Match header for ETag validation
	ifMatch := r.Header.Get("If-Match")
	if ifMatch != "" {
		// Get current state to compute the current ETag
		status, err := s.machine.GetQMPStatus()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "InternalError", "failed to get system status")
			return
		}
		powerState := mapQMPStatusToRedfish(status)
		boot := s.machine.GetBootOverride()
		currentETag := generateETag(powerState, boot)

		if ifMatch != currentETag {
			writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "ETag mismatch")
			return
		}
	}

	var req PatchSystemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid request body")
		return
	}

	if req.Boot == nil {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "no patchable properties provided")
		return
	}

	// Get current boot override and merge with patch
	current := s.machine.GetBootOverride()

	if req.Boot.BootSourceOverrideEnabled != "" {
		current.Enabled = req.Boot.BootSourceOverrideEnabled
	}
	if req.Boot.BootSourceOverrideTarget != "" {
		current.Target = req.Boot.BootSourceOverrideTarget
	}
	if req.Boot.BootSourceOverrideMode != "" {
		current.Mode = req.Boot.BootSourceOverrideMode
	}

	if err := s.machine.SetBootOverride(current); err != nil {
		writeError(w, http.StatusBadRequest, "PropertyValueError", err.Error())
		return
	}

	// Return the updated system
	s.handleGetSystem(w, r)
}

// mapQMPStatusToRedfish converts QMP status to Redfish PowerState
func mapQMPStatusToRedfish(status qmp.Status) string {
	switch status {
	case qmp.StatusRunning:
		return "On"
	default:
		return "Off"
	}
}

// generateETag creates an ETag based on the system state
func generateETag(powerState string, boot machine.BootOverride) string {
	data := fmt.Sprintf("%s-%s-%s-%s", powerState, boot.Enabled, boot.Target, boot.Mode)
	hash := sha256.Sum256([]byte(data))
	return fmt.Sprintf(`"%x"`, hash[:8])
}

// writeError writes a Redfish error response
func writeError(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(RedfishError{
		Error: RedfishErrorBody{
			Code:    code,
			Message: message,
		},
	})
}

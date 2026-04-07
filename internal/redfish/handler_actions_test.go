package redfish

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/qmp"
)

func TestResetAction(t *testing.T) {
	tests := []struct {
		name           string
		resetType      string
		expectedStatus int
		expectedCall   string
	}{
		{"On", "On", http.StatusNoContent, "On"},
		{"ForceOff", "ForceOff", http.StatusNoContent, "ForceOff"},
		{"GracefulShutdown", "GracefulShutdown", http.StatusNoContent, "GracefulShutdown"},
		{"ForceRestart", "ForceRestart", http.StatusNoContent, "ForceRestart"},
		{"GracefulRestart", "GracefulRestart", http.StatusNoContent, "GracefulRestart"},
		{"InvalidType returns 400", "InvalidType", http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockMachine(qmp.StatusRunning)
			srv := NewServer(mock, "", "", "")

			body := `{"ResetType":"` + tt.resetType + `"}`
			req := httptest.NewRequest("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.expectedCall != "" {
				require.NotEmpty(t, mock.Calls())
				assert.Equal(t, tt.expectedCall, mock.Calls()[0])
			}
		})
	}
}

package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/machine"
	"github.com/steigr/qemu-bmc/internal/qmp"
)

func TestGetSystems(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var collection SystemCollection
	err := json.Unmarshal(w.Body.Bytes(), &collection)
	require.NoError(t, err)

	assert.Equal(t, 1, collection.MembersCount)
	assert.Len(t, collection.Members, 1)
	assert.Equal(t, "/redfish/v1/Systems/1", collection.Members[0].ODataID)
}

func TestGetSystem_PowerState(t *testing.T) {
	tests := []struct {
		name           string
		qmpStatus      qmp.Status
		expectedPower  string
	}{
		{"running maps to On", qmp.StatusRunning, "On"},
		{"shutdown maps to Off", qmp.StatusShutdown, "Off"},
		{"paused maps to Off", qmp.StatusPaused, "Off"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockMachine(tt.qmpStatus)
			srv := NewServer(mock, "", "", "")

			req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			var system ComputerSystem
			err := json.Unmarshal(w.Body.Bytes(), &system)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedPower, system.PowerState)
		})
	}
}

func TestGetSystem_ETag(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify ETag header is present and non-empty
	etag := w.Header().Get("ETag")
	assert.NotEmpty(t, etag)

	// Verify @odata.etag field matches header
	var system ComputerSystem
	err := json.Unmarshal(w.Body.Bytes(), &system)
	require.NoError(t, err)
	assert.Equal(t, etag, system.ODataEtag)
}

func TestGetSystem_BootOverride(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.bootOverride = machine.BootOverride{
		Enabled: "Once",
		Target:  "Pxe",
		Mode:    "UEFI",
	}
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Systems/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var system ComputerSystem
	err := json.Unmarshal(w.Body.Bytes(), &system)
	require.NoError(t, err)

	assert.Equal(t, "Once", system.Boot.BootSourceOverrideEnabled)
	assert.Equal(t, "Pxe", system.Boot.BootSourceOverrideTarget)
	assert.Equal(t, "UEFI", system.Boot.BootSourceOverrideMode)
	assert.Contains(t, system.Boot.AllowableValues, "Pxe")
	assert.Contains(t, system.Boot.AllowableValues, "Hdd")
	assert.Contains(t, system.Boot.AllowableValues, "Cd")
}

func TestPatchBootDevice(t *testing.T) {
	t.Run("PXE Once returns 200", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		body := `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once"}}`
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var system ComputerSystem
		err := json.Unmarshal(w.Body.Bytes(), &system)
		require.NoError(t, err)

		assert.Equal(t, "Pxe", system.Boot.BootSourceOverrideTarget)
		assert.Equal(t, "Once", system.Boot.BootSourceOverrideEnabled)

		// Verify the mock was updated
		assert.Equal(t, "Pxe", mock.bootOverride.Target)
		assert.Equal(t, "Once", mock.bootOverride.Enabled)
	})

	t.Run("ETag mismatch returns 412", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		body := `{"Boot":{"BootSourceOverrideTarget":"Pxe","BootSourceOverrideEnabled":"Once"}}`
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", `"wrong-etag"`)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusPreconditionFailed, w.Code)
	})

	t.Run("No ETag returns 200", func(t *testing.T) {
		mock := newMockMachine(qmp.StatusRunning)
		srv := NewServer(mock, "", "", "")

		body := `{"Boot":{"BootSourceOverrideTarget":"Hdd","BootSourceOverrideEnabled":"Continuous"}}`
		req := httptest.NewRequest("PATCH", "/redfish/v1/Systems/1", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var system ComputerSystem
		err := json.Unmarshal(w.Body.Bytes(), &system)
		require.NoError(t, err)

		assert.Equal(t, "Hdd", system.Boot.BootSourceOverrideTarget)
		assert.Equal(t, "Continuous", system.Boot.BootSourceOverrideEnabled)
	})
}

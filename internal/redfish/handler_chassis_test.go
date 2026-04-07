package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/qmp"
)

func TestGetChassisCollection(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Chassis", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var col ChassisCollection
	err := json.Unmarshal(w.Body.Bytes(), &col)
	require.NoError(t, err)
	assert.Equal(t, 1, col.MembersCount)
	assert.Equal(t, "/redfish/v1/Chassis/1", col.Members[0].ODataID)
}

func TestGetChassis(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Chassis/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var chassis Chassis
	err := json.Unmarshal(w.Body.Bytes(), &chassis)
	require.NoError(t, err)
	assert.Equal(t, "#Chassis.v1_0_0.Chassis", chassis.ODataType)
	assert.Equal(t, "RackMount", chassis.ChassisType)
}

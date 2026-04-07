package redfish

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/steigr/qemu-bmc/internal/qmp"
)

func TestBasicAuth(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "admin", "password", "")

	t.Run("valid credentials returns 200", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/redfish/v1", nil)
		req.SetBasicAuth("admin", "password")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("wrong password returns 401", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/redfish/v1", nil)
		req.SetBasicAuth("admin", "wrong")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("no auth returns 401", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/redfish/v1", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestTrailingSlash(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	t.Run("without trailing slash returns 200", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/redfish/v1/Systems", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("with trailing slash returns 200", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/redfish/v1/Systems/", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

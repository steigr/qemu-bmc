package redfish

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/steigr/qemu-bmc/internal/machine"
	"github.com/steigr/qemu-bmc/internal/qmp"
)

func TestGetManagerCollection(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Managers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var col ManagerCollection
	err := json.Unmarshal(w.Body.Bytes(), &col)
	require.NoError(t, err)
	assert.Equal(t, 1, col.MembersCount)
	assert.Equal(t, "/redfish/v1/Managers/1", col.Members[0].ODataID)
}

func TestGetManager(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Managers/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var mgr Manager
	err := json.Unmarshal(w.Body.Bytes(), &mgr)
	require.NoError(t, err)
	assert.Equal(t, "#Manager.v1_3_0.Manager", mgr.ODataType)
	assert.Equal(t, "BMC", mgr.ManagerType)
}

func TestGetVirtualMediaCollection(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Managers/1/VirtualMedia", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var col VirtualMediaCollection
	err := json.Unmarshal(w.Body.Bytes(), &col)
	require.NoError(t, err)
	assert.Equal(t, 1, col.MembersCount)
}

func TestGetVirtualMedia_NotInserted(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Managers/1/VirtualMedia/CD1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var vm VirtualMedia
	err := json.Unmarshal(w.Body.Bytes(), &vm)
	require.NoError(t, err)
	assert.False(t, vm.Inserted)
	assert.Empty(t, vm.Image)
}

func TestInsertVirtualMedia(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "0123456789")
	}))
	defer backend.Close()

	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	body := fmt.Sprintf(`{"Image": %q, "Inserted": true}`, backend.URL+"/boot.iso")
	req := httptest.NewRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
		strings.NewReader(body))
	req.Host = "localhost:8080"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// Media is cached locally; QMP receives a local file path, not a proxy URL.
	assert.Contains(t, mock.LastInsertedMedia(), "qemu-bmc-vmedia-")
	assert.Contains(t, mock.LastInsertedMedia(), ".iso")
}

func TestInsertVirtualMedia_FirstInsertWithBootOnceCd_TriggersRestart(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "0123456789")
	}))
	defer backend.Close()

	mock := newMockMachine(qmp.StatusRunning)
	_ = mock.SetBootOverride(machine.BootOverride{Enabled: "Once", Target: "Cd", Mode: "UEFI"})
	srv := NewServer(mock, "", "", "")

	body := fmt.Sprintf(`{"Image": %q, "Inserted": true}`, backend.URL+"/boot.iso")
	req := httptest.NewRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
		strings.NewReader(body))
	req.Host = "localhost:8080"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, mock.Calls(), "InsertMedia")
	assert.Contains(t, mock.Calls(), "ForceRestart")
}

func TestInsertVirtualMedia_EmptyImage(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	body := `{"Image": "", "Inserted": true}`
	req := httptest.NewRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEjectVirtualMedia(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.lastMedia = "http://example.com/boot.iso"
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia",
		nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVirtualMedia_InsertThenGet(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "0123456789")
	}))
	defer backend.Close()

	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	// Insert
	body := fmt.Sprintf(`{"Image": %q, "Inserted": true}`, backend.URL+"/boot.iso")
	insertReq := httptest.NewRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
		strings.NewReader(body))
	insertReq.Host = "localhost:8080"
	insertReq.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, insertReq)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Get - should show inserted
	getReq := httptest.NewRequest("GET", "/redfish/v1/Managers/1/VirtualMedia/CD1", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, getReq)

	var vm VirtualMedia
	json.Unmarshal(w2.Body.Bytes(), &vm)
	assert.True(t, vm.Inserted)
	assert.Equal(t, backend.URL+"/boot.iso", vm.Image)
}

func TestVirtualMediaProxy_PassThroughRange(t *testing.T) {
	backendBody := []byte("0123456789")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(backendBody[:1])
			return
		}
		if rangeHeader == "bytes=2-5" {
			w.Header().Set("Content-Range", "bytes 2-5/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(backendBody[2:6])
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(backendBody)
	}))
	defer backend.Close()

	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	proxyPath := fmt.Sprintf("/redfish/v1/Managers/1/VirtualMedia/CD1/Proxy?image=%s", url.QueryEscape(backend.URL+"/boot.iso"))
	req := httptest.NewRequest("GET", proxyPath, nil)
	req.Header.Set("Range", "bytes=2-5")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, "bytes 2-5/10", w.Header().Get("Content-Range"))
	assert.Equal(t, "2345", w.Body.String())
}

func TestVirtualMediaProxy_CachesWhenBackendHasNoRangeSupport(t *testing.T) {
	backendBody := "abcdefghijklmnopqrstuvwxyz"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, backendBody)
	}))
	defer backend.Close()

	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	proxyPath := fmt.Sprintf("/redfish/v1/Managers/1/VirtualMedia/CD1/Proxy?image=%s", url.QueryEscape(backend.URL+"/boot.iso"))

	req1 := httptest.NewRequest("GET", proxyPath, nil)
	req1.Header.Set("Range", "bytes=10-15")
	w1 := httptest.NewRecorder()
	srv.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusPartialContent, w1.Code)
	assert.Equal(t, "klmnop", w1.Body.String())

	req2 := httptest.NewRequest("GET", proxyPath, nil)
	req2.Header.Set("Range", "bytes=0-2")
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusPartialContent, w2.Code)
	assert.Equal(t, "abc", w2.Body.String())
}

func TestVirtualMediaProxy_CachesWhenBackendClaimsAcceptRangesButIgnoresRange(t *testing.T) {
	backendBody := "abcdefghijklmnopqrstuvwxyz"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, backendBody)
	}))
	defer backend.Close()

	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	proxyPath := fmt.Sprintf("/redfish/v1/Managers/1/VirtualMedia/CD1/Proxy?image=%s", url.QueryEscape(backend.URL+"/boot.iso"))

	// A range request must still succeed via cached serving even when backend advertises
	// Accept-Ranges but does not actually return 206 for ranged reads.
	req := httptest.NewRequest("GET", proxyPath, nil)
	req.Header.Set("Range", "bytes=5-8")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, "fghi", w.Body.String())
}

func TestVirtualMediaProxy_FallsBackToCacheWhenRangedReadReturns200(t *testing.T) {
	backendBody := "abcdefghijklmnopqrstuvwxyz"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/26")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, "a")
			return
		}
		// Simulate a backend that probes as ranged-capable, but intermittently
		// ignores later Range requests by returning a full-body 200.
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, backendBody)
	}))
	defer backend.Close()

	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	proxyPath := fmt.Sprintf("/redfish/v1/Managers/1/VirtualMedia/CD1/Proxy?image=%s", url.QueryEscape(backend.URL+"/boot.iso"))
	req := httptest.NewRequest("GET", proxyPath, nil)
	req.Header.Set("Range", "bytes=10-12")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, "klm", w.Body.String())
	assert.Equal(t, "bytes", w.Header().Get("Accept-Ranges"))
}

func TestVirtualMediaProxy_AllowsAccessWithoutAuthWhenServerAuthEnabled(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-0/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "0")
	}))
	defer backend.Close()

	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "admin", "password", "")

	proxyPath := fmt.Sprintf("/redfish/v1/Managers/1/VirtualMedia/CD1/Proxy?image=%s", url.QueryEscape(backend.URL+"/boot.iso"))
	req := httptest.NewRequest("GET", proxyPath, nil)
	req.Header.Set("Range", "bytes=0-0")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusPartialContent, w.Code)
	assert.Equal(t, "0", w.Body.String())
}

func TestInsertVirtualMedia_ProxyURLDoesNotEmbedCredentials(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/10")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "0123456789")
	}))
	defer backend.Close()

	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "admin", "password", "")

	body := fmt.Sprintf(`{"Image": %q, "Inserted": true}`, backend.URL+"/boot.iso")
	req := httptest.NewRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.InsertMedia",
		strings.NewReader(body))
	req.Host = "localhost:8080"
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", "password")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, mock.LastInsertedMedia(), "admin:password@")
}

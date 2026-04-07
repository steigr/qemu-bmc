package redfish

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

type mediaProxyMode int

const (
	mediaProxyModeUnknown mediaProxyMode = iota
	mediaProxyModePassThrough
	mediaProxyModeCached
)

type mediaProxySource struct {
	imageURL  string
	mode      mediaProxyMode
	cachePath string
	mu        sync.Mutex
}

func newMediaProxySource(imageURL string) *mediaProxySource {
	return &mediaProxySource{imageURL: imageURL}
}

func (m *mediaProxySource) ensureReady(client *http.Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mode != mediaProxyModeUnknown {
		return nil
	}

	supportsRange, err := backendSupportsRange(client, m.imageURL)
	if err != nil {
		return err
	}
	if supportsRange {
		m.mode = mediaProxyModePassThrough
		return nil
	}

	cachePath, err := cacheBackendMedia(client, m.imageURL)
	if err != nil {
		return err
	}
	m.cachePath = cachePath
	m.mode = mediaProxyModeCached
	return nil
}

func (m *mediaProxySource) ensureCached(client *http.Client) error {
	m.mu.Lock()
	if m.mode == mediaProxyModeCached && m.cachePath != "" {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	cachePath, err := cacheBackendMedia(client, m.imageURL)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.cachePath = cachePath
	m.mode = mediaProxyModeCached
	m.mu.Unlock()
	return nil
}

func (m *mediaProxySource) serveCached(w http.ResponseWriter, r *http.Request) error {
	m.mu.Lock()
	cachePath := m.cachePath
	m.mu.Unlock()

	f, err := os.Open(cachePath)
	if err != nil {
		return fmt.Errorf("opening cache file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stating cache file: %w", err)
	}

	name := path.Base(cachePath)
	if parsed, parseErr := url.Parse(m.imageURL); parseErr == nil {
		if base := path.Base(parsed.Path); base != "" && base != "/" && base != "." {
			name = base
		}
	}

	http.ServeContent(w, r, name, info.ModTime(), f)
	return nil
}

func backendSupportsRange(client *http.Client, rawURL string) (bool, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return false, fmt.Errorf("creating range probe request: %w", err)
	}
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("probing range support: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusPartialContent {
		if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Range")), "bytes ") {
			return true, nil
		}
	}


	return false, nil
}

func cacheBackendMedia(client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating backend request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching backend media: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("backend returned status %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "qemu-bmc-vmedia-*.iso")
	if err != nil {
		return "", fmt.Errorf("creating cache file: %w", err)
	}
	defer func() {
		_ = tmpFile.Close()
	}()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return "", fmt.Errorf("writing cache file: %w", err)
	}

	return tmpFile.Name(), nil
}

func (s *Server) getProxySource(imageURL string) *mediaProxySource {
	s.mediaProxyMu.Lock()
	defer s.mediaProxyMu.Unlock()

	source := s.mediaProxySources[imageURL]
	if source != nil {
		return source
	}

	source = newMediaProxySource(imageURL)
	s.mediaProxySources[imageURL] = source
	return source
}

func (s *Server) buildProxyURL(r *http.Request, image string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = forwarded
	}
	host := r.Host
	if host != "" {
		if _, port, err := net.SplitHostPort(host); err == nil {
			host = net.JoinHostPort("127.0.0.1", port)
		} else if strings.Contains(err.Error(), "missing port in address") {
			if scheme == "https" {
				host = net.JoinHostPort("127.0.0.1", "443")
			} else {
				host = net.JoinHostPort("127.0.0.1", "80")
			}
		}
	}
	u := &url.URL{
		Scheme:   scheme,
		Host:     host,
		Path:     "/redfish/v1/Managers/1/VirtualMedia/CD1/Proxy",
		RawQuery: "image=" + url.QueryEscape(image),
	}
	return u.String()
}

func (s *Server) handleVirtualMediaProxy(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("image")
	if image == "" {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "image query parameter is required")
		return
	}

	parsed, err := url.Parse(image)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeError(w, http.StatusBadRequest, "ActionParameterNotSupported", "image must be an absolute http(s) URL")
		return
	}

	source := s.getProxySource(image)
	if err := source.ensureReady(s.mediaProxyClient); err != nil {
		writeError(w, http.StatusBadGateway, "InternalError", fmt.Sprintf("failed to fetch media backend: %v", err))
		return
	}

	source.mu.Lock()
	mode := source.mode
	source.mu.Unlock()

	if mode == mediaProxyModeCached {
		w.Header().Set("Accept-Ranges", "bytes")
		if err := source.serveCached(w, r); err != nil {
			writeError(w, http.StatusBadGateway, "InternalError", fmt.Sprintf("failed to serve cached media: %v", err))
		}
		return
	}

	backendReq, err := http.NewRequest(r.Method, image, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "InternalError", fmt.Sprintf("failed to create backend request: %v", err))
		return
	}
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		backendReq.Header.Set("Range", rangeHeader)
		backendReq.Header.Set("Accept-Encoding", "identity")
	}

	resp, err := s.mediaProxyClient.Do(backendReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "InternalError", fmt.Sprintf("backend request failed: %v", err))
		return
	}

	if r.Header.Get("Range") != "" && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		if err := source.ensureCached(s.mediaProxyClient); err != nil {
			writeError(w, http.StatusBadGateway, "InternalError", fmt.Sprintf("failed to cache media backend: %v", err))
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		if err := source.serveCached(w, r); err != nil {
			writeError(w, http.StatusBadGateway, "InternalError", fmt.Sprintf("failed to serve cached media: %v", err))
		}
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(resp.StatusCode)

	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return
	}
}

func newMediaProxyHTTPClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute}
}

package redfish

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func (s *Server) basicAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// QEMU's curl backend may not reliably send credentials embedded in URL.
		// Keep proxy fetches local and allow them without HTTP auth.
		if strings.HasSuffix(r.URL.Path, "/VirtualMedia/CD1/Proxy") {
			next.ServeHTTP(w, r)
			return
		}

		user, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(s.user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(s.pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Redfish"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) trailingSlashMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			r.URL.Path = strings.TrimSuffix(r.URL.Path, "/")
		}
		next.ServeHTTP(w, r)
	})
}

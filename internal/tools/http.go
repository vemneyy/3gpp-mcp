package tools

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewPDFHTTPHandler serves MCP and transient capture uploads under the same
// optional bearer token. Health probes never require authentication.
func NewPDFHTTPHandler(d *db.DB, server *mcp.Server, token string) http.Handler {
	app := http.NewServeMux()
	app.Handle("/analyze-dump", AnalyzeDumpHTTP(d))
	app.Handle("/", NewStreamableHTTPHandler(server))
	root := http.NewServeMux()
	root.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	root.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			scheme, credentials, ok := strings.Cut(r.Header.Get("Authorization"), " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(credentials)), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="3gpp-mcp"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		app.ServeHTTP(w, r)
	}))
	return root
}

package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPDFServerUnifiedTools(t *testing.T) {
	d := setupTestDB(t)
	server := NewPDFServer(d, "0.2.0")
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, tool := range listed.Tools {
		found[tool.Name] = true
		if len(tool.Description) > 1024 {
			t.Fatalf("description too long: %s", tool.Name)
		}
	}
	for _, name := range []string{"search_all", "analyze_dump", "get_rfc", "get_section"} {
		if !found[name] {
			t.Fatalf("Docker server does not expose %s", name)
		}
	}
	handler := NewPDFHTTPHandler(d, server, "secret")
	for _, path := range []string{"/", "/analyze-dump"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s is unprotected: %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health status %d", response.Code)
	}
}

package tools

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAnalyzeDumpUploads(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	d := setupTestDB(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tshark"), []byte("#!/bin/sh\nprintf 'NGAP\\t1\\t\\t1\\t\\t1\\t0.0\\tUE\\tgNB\\tInitialUEMessage\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("TMPDIR", tmp)
	capture := "\xd4\xc3\xb2\xa1"
	result, _, err := HandleAnalyzeDump(d)(t.Context(), nil, AnalyzeDumpInput{ContentBase64: base64.StdEncoding.EncodeToString([]byte(capture)), Query: "Scope"})
	if err != nil || result.IsError || !strings.Contains(getTextContent(result), "TS 23.501") {
		t.Fatalf("MCP upload: %v, %+v", err, result)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/analyze-dump?query=Scope", strings.NewReader(capture))
	AnalyzeDumpHTTP(d).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "tcp_retransmission") {
		t.Fatalf("HTTP upload: %d %s", response.Code, response.Body.String())
	}
	for _, upload := range []string{"", "not base64!"} {
		result, _, err := HandleAnalyzeDump(d)(t.Context(), nil, AnalyzeDumpInput{ContentBase64: upload})
		if err != nil || !result.IsError {
			t.Fatalf("invalid upload succeeded: %+v %v", result, err)
		}
	}
	response = httptest.NewRecorder()
	AnalyzeDumpHTTP(d).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/analyze-dump", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d", response.Code)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("uploaded capture retained: %v", entries)
	}
}

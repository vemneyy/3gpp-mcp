package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/higebu/3gpp-mcp/internal/dump"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type AnalyzeDumpInput struct {
	ContentBase64 string `json:"content_base64" jsonschema:"required,Base64-encoded bytes of a model-uploaded PCAP or PCAPNG capture (maximum decoded size 64 MiB). The caller reads the attachment and supplies its bytes; remote URLs and server file paths are not accepted. Capture bytes are processed locally and deleted before the tool returns, including failures."`
	Query         string `json:"query,omitempty" jsonschema:"Optional plain-term or boolean FTS5 query to find relevant specifications; otherwise protocols detected locally determine the query."`
}

type DumpAnalysis struct {
	Capture        *dump.Report             `json:"capture"`
	Specifications *db.UnifiedSearchResults `json:"specifications,omitempty"`
	Warnings       []string                 `json:"warnings,omitempty"`
}

var AnalyzeDumpTool = &mcp.Tool{
	Name:        "analyze_dump",
	Description: "Analyze a model-uploaded PCAP/PCAPNG capture locally with tshark and search related specifications across clause text and OpenAPI in one call. Supply capture bytes as content_base64. Reports protocol counts, bounded decoded packet summaries, TCP retransmissions/resets, capture gaps and malformed packets, then links to relevant specification sections or API definitions. Requires local tshark. Analysis is bounded to 64 MiB, 10,000 packets and 60 seconds. Capture files are temporary and deleted on success, error or cancellation; no capture is stored in the database or cache. Findings are observations, not a standards-compliance verdict. For larger captures, HTTP servers also accept raw bytes at POST /analyze-dump. The caller controls retention in its own upload/chat system.",
}

// AnalyzeCapture shares local analysis and specification lookup with the CLI.
func AnalyzeCapture(ctx context.Context, d *db.DB, report *dump.Report, query string) (*DumpAnalysis, error) {
	result := &DumpAnalysis{Capture: report}
	if strings.TrimSpace(query) == "" {
		query = strings.Join(report.SpecificationQueries, " OR ")
	}
	if query != "" {
		specs, err := d.SearchAll(ctx, query, nil, 10, 0)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("specification lookup failed: %v", err))
		} else {
			result.Specifications = specs
		}
	}
	return result, nil
}

func HandleAnalyzeDump(d *db.DB) func(context.Context, *mcp.CallToolRequest, AnalyzeDumpInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AnalyzeDumpInput) (*mcp.CallToolResult, any, error) {
		if in.ContentBase64 == "" {
			return errorResult("content_base64 is required"), nil, nil
		}
		if len(in.ContentBase64) > base64.StdEncoding.EncodedLen(dump.MaxBytes)+1024 {
			return errorResult("encoded capture exceeds 64 MiB limit"), nil, nil
		}
		reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(in.ContentBase64))
		report, err := dump.Analyze(ctx, reader)
		if err != nil {
			return errorResult(fmt.Sprintf("capture analysis failed: %v", err)), nil, nil
		}
		result, err := AnalyzeCapture(ctx, d, report, in.Query)
		if err != nil {
			return nil, nil, err
		}
		data, err := json.Marshal(result)
		if err != nil {
			return nil, nil, err
		}
		return textResult(string(data)), nil, nil
	}
}

// AnalyzeDumpHTTP accepts a single binary upload and returns the same report as
// the MCP tool. It does not create an upload ID or retain capture data for later
// calls. The transport's existing bearer middleware protects this endpoint.
func AnalyzeDumpHTTP(d *db.DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST a PCAP or PCAPNG capture", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(60 * time.Second))
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, dump.MaxBytes)
		report, err := dump.Analyze(r.Context(), r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("capture analysis failed: %v", err), http.StatusBadRequest)
			return
		}
		result, err := AnalyzeCapture(r.Context(), d, report, r.URL.Query().Get("query"))
		if err != nil {
			http.Error(w, "specification lookup failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}

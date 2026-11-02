package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SearchAllInput struct {
	Query   string   `json:"query" jsonschema:"required,FTS5 query shared by clause text and API definitions. Prefer plain terms or quoted phrases with AND/OR/NOT; index-specific column filters are not supported across both indexes."`
	SpecIDs []string `json:"spec_ids,omitempty" jsonschema:"Optional specification IDs; omitted searches every indexed specification."`
	Limit   int      `json:"limit,omitempty" jsonschema:"Page size (default 10, maximum 200)."`
	Offset  int      `json:"offset,omitempty" jsonschema:"Global pagination offset."`
}

var SearchAllTool = &mcp.Tool{
	Name:        "search_all",
	Description: "Search all indexed specifications in one call, including both clause text and OpenAPI schemas and operations. Use this for a topic, protocol, message, data type or endpoint without choosing separate search commands. Results alternate between the independently ranked indexes and identify source, spec_id and either a section number or API name and definition. Follow up with get_section or get_openapi. The current database version of each specification is searched; archived versions and meeting documents are excluded. Missing OpenAPI indexes are reported in warnings. Use plain terms, quoted phrases and boolean operators shared by both indexes.",
}

func HandleSearchAll(d *db.DB) func(context.Context, *mcp.CallToolRequest, SearchAllInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchAllInput) (*mcp.CallToolResult, any, error) {
		results, err := d.SearchAll(ctx, in.Query, in.SpecIDs, in.Limit, in.Offset)
		if err != nil {
			return errorResult(fmt.Sprintf("search failed: %v", err)), nil, nil
		}
		data, err := json.Marshal(results)
		if err != nil {
			return nil, nil, err
		}
		return textResult(string(data)), nil, nil
	}
}

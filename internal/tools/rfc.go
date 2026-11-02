package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
)

type RFCInput struct {
	Number   int    `json:"number" jsonschema:"required,RFC number such as 9110"`
	Section  string `json:"section,omitempty" jsonschema:"Section number; omit to list sections"`
	Offset   int    `json:"offset,omitempty" jsonschema:"Starting line for pagination"`
	MaxLines int    `json:"max_lines,omitempty" jsonschema:"Maximum lines, default 200"`
}

var RFCTool = &mcp.Tool{Name: "get_rfc", Description: "Read an indexed RFC by number. Omit section to list its table of contents, then provide a section number to read it. RFCs are immutable publications; consult RFC Editor for current status and updates."}

func HandleRFC(d *db.DB) func(context.Context, *mcp.CallToolRequest, RFCInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RFCInput) (*mcp.CallToolResult, any, error) {
		if in.Number < 1 {
			return errorResult("positive RFC number required"), nil, nil
		}
		id := fmt.Sprintf("RFC %d", in.Number)
		if in.Section == "" {
			v, e := d.GetTOC(ctx, id, "")
			if e != nil {
				return errorResult(e.Error()), nil, nil
			}
			if len(v) == 0 {
				return errorResult(id + " not indexed"), nil, nil
			}
			b, _ := json.Marshal(v)
			return textResult(string(b)), nil, nil
		}
		v, e := d.GetSection(ctx, id, "", in.Section, false)
		if e != nil {
			return errorResult(e.Error()), nil, nil
		}
		if len(v) == 0 {
			return errorResult("RFC section not found"), nil, nil
		}
		return prependLine("[Source: "+id+" — Section "+in.Section+"]", paginateText(v[0].Content, in.Offset, in.MaxLines, 0)), nil, nil
	}
}

type RFCSearchInput struct {
	Query  string `json:"query" jsonschema:"required,Words or phrase to find in RFC text"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum results, default 10"`
	Offset int    `json:"offset,omitempty" jsonschema:"Pagination offset"`
}

var RFCSearchTool = &mcp.Tool{Name: "search_rfc", Description: "Search indexed RFC sections for a phrase. Returns RFC numbers, section numbers and snippets; use get_rfc to read a result. Search uses token matching with English stemming."}

func HandleRFCSearch(d *db.DB) func(context.Context, *mcp.CallToolRequest, RFCSearchInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RFCSearchInput) (*mcp.CallToolResult, any, error) {
		q := strings.TrimSpace(in.Query)
		if q == "" || len(q) > 500 {
			return errorResult("query must contain 1 to 500 bytes"), nil, nil
		}
		q = `spec_id:RFC AND "` + strings.ReplaceAll(q, `"`, `""`) + `"`
		result, err := d.Search(ctx, q, nil, in.Limit, in.Offset)
		if err != nil {
			return errorResult(err.Error()), nil, nil
		}
		b, err := json.Marshal(result)
		if err != nil {
			return nil, nil, err
		}
		return textResult(string(b)), nil, nil
	}
}

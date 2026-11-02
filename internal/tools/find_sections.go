package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type FindSectionsInput struct {
	Terms   []string `json:"terms" jsonschema:"required,Words or phrases to find, for example [AMF, authentication]. Each item is treated as a phrase; no query syntax is needed."`
	Match   string   `json:"match,omitempty" jsonschema:"all (default) requires every phrase in the same section; any requires at least one. These are token matches with English stemming, not exact substring matches."`
	Scope   string   `json:"scope,omitempty" jsonschema:"text (default) searches section content and headings; title searches headings only."`
	SpecIDs []string `json:"spec_ids,omitempty" jsonschema:"Optional specification IDs to narrow the search, for example [TS 23.501]."`
	Limit   int      `json:"limit,omitempty" jsonschema:"Results per page, default 10, maximum 50."`
	Offset  int      `json:"offset,omitempty" jsonschema:"Pagination offset; use next_offset from the response."`
}

var FindSectionsTool = &mcp.Tool{Name: "find_sections", Description: "Find sections mentioning words or phrases without writing search syntax. Use match=all to locate sections containing all requested concepts, match=any to explore alternatives, and scope=title to find sections devoted to a topic. Results include snippets, source version and ready-to-use get_section arguments. Matches are token-based with English stemming; absence of matches does not prove absence of a concept. Read the returned sections before answering or citing them."}

func HandleFindSections(d *db.DB) func(context.Context, *mcp.CallToolRequest, FindSectionsInput) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input FindSectionsInput) (*mcp.CallToolResult, any, error) {
		if len(input.Terms) == 0 || len(input.Terms) > 20 {
			return errorResult("provide 1 to 20 non-empty terms or phrases"), nil, nil
		}
		operator := " AND "
		if input.Match == "any" {
			operator = " OR "
		} else if input.Match != "" && input.Match != "all" {
			return errorResult("match must be all or any"), nil, nil
		}
		if input.Scope != "" && input.Scope != "text" && input.Scope != "title" {
			return errorResult("scope must be text or title"), nil, nil
		}
		phrases := make([]string, 0, len(input.Terms))
		for _, term := range input.Terms {
			term = strings.TrimSpace(term)
			if term == "" || len(term) > 500 {
				return errorResult("each term must contain 1 to 500 bytes"), nil, nil
			}
			phrases = append(phrases, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		}
		query := strings.Join(phrases, operator)
		if input.Scope == "title" {
			for i := range phrases {
				phrases[i] = "title:" + phrases[i]
			}
			query = strings.Join(phrases, operator)
		}
		limit := input.Limit
		if limit <= 0 {
			limit = 10
		}
		if limit > 50 {
			limit = 50
		}
		offset := max(input.Offset, 0)
		result, err := d.Search(ctx, query, input.SpecIDs, limit, offset)
		if err != nil {
			return errorResult(fmt.Sprintf("find_sections failed: %v", err)), nil, nil
		}
		type readArgs struct {
			SpecID        string `json:"spec_id"`
			Version       string `json:"version"`
			SectionNumber string `json:"section_number"`
		}
		type hit struct {
			db.SearchResult
			Read readArgs `json:"get_section_arguments"`
		}
		hits := make([]hit, 0, len(result.Results))
		for _, r := range result.Results {
			hits = append(hits, hit{r, readArgs{r.SpecID, r.Version, r.Number}})
		}
		var next *int
		if offset+len(hits) < result.TotalCount {
			n := offset + len(hits)
			next = &n
		}
		data, err := json.Marshal(struct {
			Results []hit `json:"results"`
			Total   int   `json:"total_count"`
			Next    *int  `json:"next_offset"`
		}{hits, result.TotalCount, next})
		if err != nil {
			return nil, nil, err
		}
		return textResult(string(data)), nil, nil
	}
}

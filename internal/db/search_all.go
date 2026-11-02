package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// UnifiedSearchResult identifies the index and the follow-up tool for a hit.
type UnifiedSearchResult struct {
	Source  string `json:"source"`
	SpecID  string `json:"spec_id"`
	Version string `json:"version,omitempty"`
	Release string `json:"release,omitempty"`
	Number  string `json:"number,omitempty"`
	Title   string `json:"title,omitempty"`
	APIName string `json:"api_name,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Name    string `json:"name,omitempty"`
	Snippet string `json:"snippet"`
}

type UnifiedSearchResults struct {
	Results    []UnifiedSearchResult `json:"results"`
	TotalCount int                   `json:"total_count"`
	Limit      int                   `json:"limit"`
	Offset     int                   `json:"offset"`
	Warnings   []string              `json:"warnings,omitempty"`
}

// SearchAll searches clause text and OpenAPI definitions across all indexed
// specifications. Results alternate between independently ranked indexes,
// whose BM25 scores cannot be compared. Pagination never loads earlier pages.
// Archived versions and TDocs are intentionally outside the searchable corpus.
func (d *DB) SearchAll(ctx context.Context, query string, specIDs []string, limit, offset int) (*UnifiedSearchResults, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	if offset < 0 {
		offset = 0
	}
	sections, err := d.Search(ctx, query, specIDs, 1, 0)
	if err != nil {
		return nil, err
	}
	apis, err := d.SearchOpenAPI(ctx, query, specIDs, "", "", false, 1, 0)
	res := &UnifiedSearchResults{Results: []UnifiedSearchResult{}, Limit: limit, Offset: offset}
	if errors.Is(err, ErrNoOpenAPIIndex) {
		apis = &OpenAPISearchResults{}
		res.Warnings = []string{"OpenAPI index unavailable; run build-openapi-index to include API definitions."}
	} else if err != nil {
		return nil, err
	}
	n, m := sections.TotalCount, apis.TotalCount
	res.TotalCount = n + m
	// Number of section hits before a given global offset in the interleaving.
	before := func(pos int) int {
		pos = min(pos, n+m)
		paired := min(n, m)
		if pos <= 2*paired {
			return (pos + 1) / 2
		}
		if n > m {
			return paired + pos - 2*paired
		}
		return paired
	}
	end := offset + min(limit, max(0, res.TotalCount-offset))
	startS, endS := before(offset), before(end)
	startA, endA := min(offset, n+m)-startS, min(end, n+m)-endS
	var sh []SearchResult
	var ah []OpenAPISearchResult
	if endS > startS {
		page, err := d.Search(ctx, query, specIDs, endS-startS, startS)
		if err != nil {
			return nil, err
		}
		sh = page.Results
	}
	if endA > startA {
		page, err := d.SearchOpenAPI(ctx, query, specIDs, "", "", false, endA-startA, startA)
		if err != nil {
			return nil, err
		}
		ah = page.Results
	}
	si, ai := 0, 0
	for pos := offset; pos < end; pos++ {
		if before(pos+1) > before(pos) {
			if si >= len(sh) {
				return nil, fmt.Errorf("search corpus changed during pagination; retry")
			}
			h := sh[si]
			si++
			res.Results = append(res.Results, UnifiedSearchResult{Source: "section", SpecID: h.SpecID, Version: h.Version, Release: h.Release, Number: h.Number, Title: h.Title, Snippet: h.Snippet})
		} else {
			if ai >= len(ah) {
				return nil, fmt.Errorf("search corpus changed during pagination; retry")
			}
			h := ah[ai]
			ai++
			res.Results = append(res.Results, UnifiedSearchResult{Source: "openapi", SpecID: h.SpecID, APIName: h.APIName, Kind: h.Kind, Name: h.Name, Snippet: h.Snippet})
		}
	}
	return res, nil
}

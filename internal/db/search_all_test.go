package db

import (
	"reflect"
	"testing"
)

func TestSearchAllPagination(t *testing.T) {
	d := setupOpenAPIIndexDB(t)
	for _, query := range []string{"Scope OR NFProfile", "NFProfile", "Scope", "noSuchToken"} {
		t.Run(query, func(t *testing.T) {
			all, err := d.SearchAll(t.Context(), query, nil, 200, 0)
			if err != nil {
				t.Fatal(err)
			}
			var paged []UnifiedSearchResult
			for offset := 0; offset < all.TotalCount+2; offset++ {
				page, err := d.SearchAll(t.Context(), query, nil, 1, offset)
				if err != nil {
					t.Fatal(err)
				}
				if page.TotalCount != all.TotalCount {
					t.Fatalf("total changed: %+v", page)
				}
				paged = append(paged, page.Results...)
			}
			if len(paged) != len(all.Results) {
				t.Fatalf("pagination count: %d != %d", len(paged), len(all.Results))
			}
			for i := range paged {
				if !reflect.DeepEqual(paged[i], all.Results[i]) {
					t.Fatalf("page %d differs", i)
				}
			}
		})
	}
	page, err := d.SearchAll(t.Context(), "Scope OR NFProfile", []string{"TS 29.510"}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.TotalCount != 3 || page.Results[0].Source != "section" || page.Results[1].Source != "openapi" {
		t.Fatalf("unexpected mixed results: %+v", page)
	}
}

func TestSearchAllMissingIndex(t *testing.T) {
	d := setupTestDB(t)
	if err := d.DropOpenAPIIndex(); err != nil {
		t.Fatal(err)
	}
	result, err := d.SearchAll(t.Context(), "Scope", nil, 0, -3)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 2 || len(result.Warnings) != 1 || result.Offset != 0 {
		t.Fatalf("unexpected fallback: %+v", result)
	}
	if _, err := d.SearchAll(t.Context(), " ", nil, 0, 0); err == nil {
		t.Fatal("empty query accepted")
	}
}

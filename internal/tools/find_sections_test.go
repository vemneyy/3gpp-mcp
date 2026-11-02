package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFindSections(t *testing.T) {
	d, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		t.Fatal(err)
	}
	spec := db.Spec{ID: "TS 23.501", Version: "19.9.0", Title: "Architecture"}
	if err = d.InsertSpecWithSections(spec, []db.Section{{SpecID: spec.ID, Number: "1", Title: "Authentication", Content: "AMF authentication procedure"}, {SpecID: spec.ID, Number: "2", Title: "Other", Content: "AMF routing"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input FindSectionsInput
		want  int
	}{{FindSectionsInput{Terms: []string{"AMF", "authentication"}}, 1}, {FindSectionsInput{Terms: []string{"AMF", "authentication"}, Match: "any"}, 2}, {FindSectionsInput{Terms: []string{"authentication"}, Scope: "title"}, 1}, {FindSectionsInput{Terms: []string{"AMF"}, Scope: "title"}, 0}} {
		result, _, err := HandleFindSections(d)(context.Background(), nil, tc.input)
		if err != nil || result.IsError {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		var decoded struct {
			Total   int `json:"total_count"`
			Results []struct {
				Read struct {
					ID     string `json:"spec_id"`
					Number string `json:"section_number"`
				} `json:"get_section_arguments"`
			} `json:"results"`
		}
		if err = json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Total != tc.want {
			t.Fatalf("total %d want %d", decoded.Total, tc.want)
		}
		if len(decoded.Results) > 0 && decoded.Results[0].Read.ID != spec.ID {
			t.Fatal("missing read arguments")
		}
	}
}

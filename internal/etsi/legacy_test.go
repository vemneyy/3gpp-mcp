package etsi

import (
	"fmt"
	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/higebu/3gpp-mcp/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyScopeNumbering(t *testing.T) {
	for _, number := range []string{"0", "0.1", "1.1"} {
		t.Run(number, func(t *testing.T) {
			parent := ""
			if strings.Contains(number, ".") {
				parent = strings.Split(number, ".")[0] + " Introduction\nIntroductory prose retained before Scope.\n"
			}
			input := "Contents\n" + number + " Scope ..... 5\n2 References ..... 6\n\f" + parent + number + " Scope\nThis is the actual scope body.\n2 References\nReference text.\n"
			_, sections, err := ParseText(Document{ID: "GSM 04.01", Version: "8.0.0"}, input)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, section := range sections {
				if section.Number == number {
					found = true
					if !strings.Contains(section.Content, "actual scope body") {
						t.Fatalf("TOC selected: %+v", section)
					}
				}
			}
			if !found {
				t.Fatalf("original scope number %s lost", number)
			}
			if parent != "" && !strings.Contains(sections[0].Content, "Introductory prose") {
				t.Fatal("parent heading or its text lost")
			}
		})
	}
}

func TestBareContentsNotBody(t *testing.T) {
	input := "Contents\n0 Scope\n1 General\n\f0 Scope\nReal body.\n1 General\nGeneral text.\n"
	_, sections, err := ParseText(Document{ID: "GSM 02.01", Version: "8.2.1"}, input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sections[0].Content, "Real body") || strings.Contains(sections[0].Content, "\n1 General") {
		t.Fatalf("bad body boundary: %+v", sections)
	}
}

func TestLegacyCoverIdentity(t *testing.T) {
	for _, designation := range []string{"GSM 02.01", "3GPP TS 03.08"} {
		t.Run(designation, func(t *testing.T) {
			text := fmt.Sprintf("ETSI TS 100 500 V8.2.1\nTechnical Specification\nDigital cellular telecommunications system (Phase 2+);\nActual publication title\n(%s version 8.2.1 Release 1999)\n\f0 Scope\nScope text.\n1 General\nBody text.\n", designation)
			spec, sections, err := ParseText(Document{ID: "TS 00.500", Series: "00", Version: "8.2.1"}, text)
			if err != nil {
				t.Fatal(err)
			}
			expected := strings.TrimPrefix(designation, "3GPP ")
			if spec.ID != expected || spec.Series == "00" || spec.Title != "Actual publication title" {
				t.Fatalf("cover identity or metadata: %+v", spec)
			}
			if sections[0].SpecID != expected {
				t.Fatal("section identity disagrees")
			}
		})
	}
}

func TestImportMigratesGSMSourceAlias(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'ETSI TS 100 500 V8.2.1\\nTechnical Specification\\nGSM publication title\\n(GSM 02.01 version 8.2.1 Release 1999)\\n\\f0 Scope\\nScope body.\\n1 General\\nGeneral body.\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "pdftotext"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("%PDF-1.7\nfixture")) }))
	defer server.Close()
	d := testutil.SetupTestDB(t)
	old := db.Spec{ID: "ETSI TS 100500", Version: "8.2.1", Title: "Old alias"}
	if err := d.InsertSpecWithSections(old, []db.Section{{SpecID: old.ID, Version: old.Version, Number: "1", Title: "Scope", Content: "Old alias text"}}); err != nil {
		t.Fatal(err)
	}
	if err := (Client{HTTP: server.Client()}).Import(t.Context(), d, Document{ID: "TS 00.500", Series: "00", Version: "8.2.1", URL: server.URL + "/ts_100500v080201p.pdf"}); err != nil {
		t.Fatal(err)
	}
	oldVersions, err := d.ListSpecVersions(t.Context(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := d.ListSpecVersions(t.Context(), "GSM 02.01")
	if err != nil {
		t.Fatal(err)
	}
	if len(oldVersions) != 0 || len(versions) != 1 {
		t.Fatalf("migration: old=%v new=%v", oldVersions, versions)
	}
}

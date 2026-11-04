package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitGlossaryExtraction(t *testing.T) {
	entries := glossaryEntries("Abbreviations", "For the purposes of this document:\n\n5GC   5G Core Network\nAMF   Access and Mobility Management Function\nMME   Mobility Management Entity\n")
	if len(entries) != 3 || entries[0].Term != "5GC" || entries[1].Definition != "Access and Mobility Management Function" {
		t.Fatalf("%+v", entries)
	}
	entries = glossaryEntries("Definitions", "access network: An access network connecting the UE\nto the core network.\n\nUE: User Equipment\n")
	if len(entries) != 2 || entries[0].Definition != "An access network connecting the UE to the core network." {
		t.Fatalf("%+v", entries)
	}
	if len(glossaryEntries("Attach procedure", "MME   Mobility Management Entity")) != 0 {
		t.Fatal("extracted from ordinary prose")
	}
	for title, kind := range map[string]string{"Annex G (informative): Change history": "history", "References": "references", "Definitions": "glossary", "Initial attach": "content"} {
		if sectionKind(title) != kind {
			t.Fatal(title)
		}
	}
}
func TestV2EnrichmentMigration(t *testing.T) {
	ctx := context.Background()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "v2.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema, e := os.ReadFile("testdata/v2.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(schema)); e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`INSERT INTO documents VALUES(1,'23.401','3gpp','23','LTE EPS architecture','19.0.0','https://www.etsi.org/deliver/etsi_ts/123400_123499/123401/file.pdf','hash');
 INSERT INTO sections VALUES(1,1,'3.2','Abbreviations',42); INSERT INTO section_text VALUES(1,'MME   Mobility Management Entity');`)
	if e != nil {
		t.Fatal(e)
	}
	if e = ensureSchema(ctx, db, true); e != nil {
		t.Fatal(e)
	}
	var term, etsi string
	var version int
	if e = db.QueryRow("SELECT term FROM glossary").Scan(&term); e != nil || term != "MME" {
		t.Fatal(term, e)
	}
	if e = db.QueryRow("SELECT etsi_number FROM document_search").Scan(&etsi); e != nil || etsi != "123401" {
		t.Fatal(etsi, e)
	}
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 3 {
		t.Fatal(version, e)
	}
	if e = ensureSchema(ctx, db, true); e != nil {
		t.Fatal(e)
	}
}

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyMigrationWithoutDownloads(t *testing.T) {
	ctx := context.Background()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema, e := os.ReadFile("testdata/legacy.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(schema)); e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`INSERT INTO documents VALUES(1,'23.501','3gpp','23','Architecture','19.0.0','https://www.etsi.org/test.pdf','hash');
 INSERT INTO sections VALUES(10,1,'1','PDU session','session body',42,'pdu session','session body');
 INSERT INTO term_counts VALUES('session',10,1,1);`)
	if e != nil {
		t.Fatal(e)
	}
	if e = ensureSchema(ctx, db, false); e == nil {
		t.Fatal("implicit migration accepted")
	}
	if e = ensureSchema(ctx, db, true); e != nil {
		t.Fatal(e)
	}
	if e = ensureSchema(ctx, db, true); e != nil {
		t.Fatal("idempotent migration", e)
	}
	var body string
	var page, hits int
	if e = db.QueryRow("SELECT t.body,s.page FROM sections s JOIN section_text t ON t.section_id=s.id WHERE s.id=10").Scan(&body, &page); e != nil || body != "session body" || page != 42 {
		t.Fatalf("data lost: %s %d %v", body, page, e)
	}
	if e = db.QueryRow("SELECT title_hits+body_hits FROM postings WHERE term='session' AND section_id=10").Scan(&hits); e != nil || hits != 2 {
		t.Fatalf("counts lost: %d %v", hits, e)
	}
	if e = db.QueryRow("SELECT section_count FROM term_stats WHERE term='session'").Scan(&hits); e != nil || hits != 1 {
		t.Fatalf("stats wrong: %d %v", hits, e)
	}
	if _, e = db.Exec("DELETE FROM documents"); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow("SELECT count(*) FROM term_stats").Scan(&hits); e != nil || hits != 0 {
		t.Fatalf("cascade stale: %d %v", hits, e)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "broken.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema, e := os.ReadFile("testdata/legacy.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(schema)); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("DROP TABLE term_counts"); e != nil {
		t.Fatal(e)
	}
	if e = ensureSchema(ctx, db, true); e == nil {
		t.Fatal("expected missing legacy index error")
	}
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 0 {
		t.Fatalf("partial schema committed: %d %v", version, e)
	}
	if _, e = db.Exec("SELECT body FROM sections"); e != nil {
		t.Fatal("original table lost", e)
	}
}

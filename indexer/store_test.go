package main

import (
	"context"
	"database/sql"
	"testing"
)

func TestAtomicReplacementAndCounts(t *testing.T) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema, _ := assets.ReadFile("schema.sql")
	if _, e = db.Exec(string(schema)); e != nil {
		t.Fatal(e)
	}
	c := crawler{db: db}
	ctx := context.Background()
	d := document{Spec: "23.501", Family: "3gpp", Series: "23", Version: "19.0.0", Sections: []section{{Number: "1", Title: "PDU-session", Body: "PDU session PDU session", Page: 3}}}
	if e = c.store(ctx, d); e != nil {
		t.Fatal(e)
	}
	var title, body int
	if e = db.QueryRow("SELECT title_hits,body_hits FROM postings WHERE term='pdu'").Scan(&title, &body); e != nil || title != 1 || body != 2 {
		t.Fatalf("counts %d %d %v", title, body, e)
	}
	d.Version = "20.0.0"
	d.Sections[0].Body = "replacement"
	if e = c.store(ctx, d); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = db.QueryRow("SELECT count(*) FROM postings WHERE term='replacement'").Scan(&count); e != nil || count != 1 {
		t.Fatalf("postings stale %v %d", e, count)
	}
	if e = db.QueryRow("SELECT sum(body_hits) FROM postings WHERE term='pdu'").Scan(&count); e != nil || count != 0 {
		t.Fatalf("counts stale %v %d", e, count)
	}
	d.Version = "21.0.0"
	d.Sections = append(d.Sections, d.Sections[0]) // duplicate section causes rollback
	if e = c.store(ctx, d); e == nil {
		t.Fatal("expected transaction failure")
	}
	var version string
	if e = db.QueryRow("SELECT version FROM documents").Scan(&version); e != nil || version != "20.0.0" {
		t.Fatalf("rollback lost document: %s %v", version, e)
	}
}

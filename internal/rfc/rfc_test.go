package rfc

import (
	"context"
	"fmt"
	"github.com/higebu/3gpp-mcp/internal/db"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexRFC(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rfc-index.xml":
			fmt.Fprint(w, `<rfc-index xmlns="https://www.rfc-editor.org/rfc-index"><rfc-entry><doc-id>RFC9110</doc-id><title>HTTP Semantics</title></rfc-entry></rfc-index>`)
		case "/rfc/rfc9110.txt":
			fmt.Fprint(w, "HTTP Semantics\n\n1.  Introduction\nHTTP semantics define resources.\n\n2.  Architecture\nMessages.\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := Client{Base: s.URL}
	entries, err := c.Discover(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("%+v %v", entries, err)
	}
	d, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "rfc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		t.Fatal(err)
	}
	if err = c.Import(context.Background(), d, entries[0]); err != nil {
		t.Fatal(err)
	}
	secs, err := d.GetSection(context.Background(), "RFC 9110", "", "1", false)
	if err != nil || len(secs) != 1 {
		t.Fatalf("%+v %v", secs, err)
	}
	res, err := d.Search(context.Background(), `spec_id:RFC AND "semantics"`, nil, 10, 0)
	if err != nil || len(res.Results) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestLiveRFC(t *testing.T) {
	if os.Getenv("RFC_LIVE_TEST") == "" {
		t.Skip("set RFC_LIVE_TEST=1")
	}
	c := Client{}
	ctx := context.Background()
	entries, err := c.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var entry Entry
	for _, e := range entries {
		if e.ID == "RFC9110" {
			entry = e
			break
		}
	}
	if entry.ID == "" {
		t.Fatal("RFC 9110 missing from catalogue")
	}
	d, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		t.Fatal(err)
	}
	if err = c.Import(ctx, d, entry); err != nil {
		t.Fatal(err)
	}
	sec, err := d.GetSection(ctx, "RFC 9110", "", "1", false)
	if err != nil || len(sec) == 0 {
		t.Fatalf("section 1: %+v %v", sec, err)
	}
}

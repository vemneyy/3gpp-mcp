package etsi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/higebu/3gpp-mcp/internal/db"
	"strings"
	"testing"
)

func TestDiscoverLatestAndTypes(t *testing.T) {
	pages := map[string]string{
		"/etsi_ts/":                                  `<a href="123500_123599/">series</a><a href="999999_999999/">other</a>`,
		"/etsi_ts/123500_123599/":                    `<a href="123501/">spec</a>`,
		"/etsi_ts/123500_123599/123501/":             `<a href="17.01.00_60/">old</a><a href="18.02.00_60/">new</a>`,
		"/etsi_ts/123500_123599/123501/18.02.00_60/": `<a href="ts_123501v180200p.pdf">pdf</a>`,
		"/etsi_tr/":                                  `<a href="123800_123899/">series</a>`,
		"/etsi_tr/123800_123899/":                    `<a href="123899/">spec</a>`,
		"/etsi_tr/123800_123899/123899/":             `<a href="19.00.00_60/">v</a>`,
		"/etsi_tr/123800_123899/123899/19.00.00_60/": `<a href="tr_123899v190000p.pdf">pdf</a>`,
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, p)
	}))
	defer s.Close()
	docs, err := (Client{Base: s.URL}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].ID != "TS 23.501" || docs[0].Version != "18.2.0" || docs[1].ID != "TR 23.899" {
		t.Fatalf("documents: %+v", docs)
	}
}
func TestDiscoverRejectsBlockedListing(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "This transfer is blocked") }))
	defer s.Close()
	if _, err := (Client{Base: s.URL}).Discover(context.Background()); err == nil {
		t.Fatal("blocked listing accepted")
	}
}
func TestParseTextSkipsContents(t *testing.T) {
	doc := Document{ID: "TS 23.501", Version: "18.2.0", Series: "23"}
	input := "Contents\n1 Scope ..... 7\n2 References ..... 8\n\fETSI TS 123 501 V18.2.0\n1 Scope\nThis document describes the system.\n2 References\nReferences text\n2.1 Normative references\nMore text\n"
	spec, sections, err := ParseText(doc, input)
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != doc.ID || len(sections) != 3 || sections[2].ParentNumber != "2" || !strings.Contains(sections[0].Content, "system") {
		t.Fatalf("spec=%+v sections=%+v", spec, sections)
	}
}

func TestImportStoresSearchableSections(t *testing.T) {
	dir := t.TempDir()
	extractor := filepath.Join(dir, "pdftotext")
	if err := os.WriteFile(extractor, []byte("#!/bin/sh\nprintf '(3GPP TS 23.501 version 18.2.0 Release 18)\\n\\f1 Scope\\nUnique searchable architecture text.\\n2 References\\nNo references.\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "%PDF-1.7\ntest") }))
	defer s.Close()
	d, err := db.OpenReadWrite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		t.Fatal(err)
	}
	doc := Document{ID: "TS 23.501", Version: "18.2.0", Series: "23", URL: s.URL + "/ts_123501v180200p.pdf"}
	if err = (Client{}).Import(context.Background(), d, doc); err != nil {
		t.Fatal(err)
	}
	results, err := d.Search(context.Background(), "architecture", nil, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results.Results) != 1 || results.Results[0].SpecID != doc.ID {
		t.Fatalf("search results: %+v", results)
	}
}

// Set ETSI_INTEGRATION_PDF to run a live end-to-end test using the official PDF.
func TestLivePDFImport(t *testing.T) {
	address := os.Getenv("ETSI_INTEGRATION_PDF")
	if address == "" {
		t.Skip("set ETSI_INTEGRATION_PDF to test the live ETSI PDF")
	}
	d, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		t.Fatal(err)
	}
	doc := Document{ID: "TS 23.501", Version: "18.5.0", Series: "23", URL: address}
	if err = (Client{}).Import(context.Background(), d, doc); err != nil {
		t.Fatal(err)
	}
	res, err := d.Search(context.Background(), "architecture", nil, 10, 0)
	if err != nil || len(res.Results) == 0 {
		t.Fatalf("no searchable PDF text: %+v, %v", res, err)
	}
}

func TestDiscoverPartAndNewestRelease(t *testing.T) {
	pages := map[string]string{
		"/etsi_ts/":                                    `<a href="138100_138199/">group</a>`,
		"/etsi_ts/138100_138199/":                      `<a href="13810101/">part</a>`,
		"/etsi_ts/138100_138199/13810101/":             `<a href="18.20.00_60/">old</a><a href="19.06.00_60/">latest</a>`,
		"/etsi_ts/138100_138199/13810101/19.06.00_60/": `<a href="ts_13810101v190600p.pdf">PDF</a>`,
		"/etsi_tr/":                                    `<a href="123800_123899/">group</a>`,
		"/etsi_tr/123800_123899/":                      `<a href="123899/">spec</a>`,
		"/etsi_tr/123800_123899/123899/":               `<a href="19.00.00_60/">version</a>`,
		"/etsi_tr/123800_123899/123899/19.00.00_60/":   `<a href="tr_123899v190000p.pdf">PDF</a>`,
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, p)
	}))
	defer s.Close()
	docs, err := (Client{Base: s.URL}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Version != "19.6.0" || !strings.HasSuffix(docs[0].URL, "ts_13810101v190600p.pdf") {
		t.Fatalf("incorrect latest version or part: %+v", docs)
	}
}

func TestImportRejectsHTTPForbidden(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "blocked", http.StatusForbidden) }))
	defer s.Close()
	err := (Client{}).Import(context.Background(), nil, Document{URL: s.URL})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected HTTP 403, got %v", err)
	}
}

// ETSI_LIVE_LATEST runs discovery and ingestion against a real ETSI specification.
func TestLiveLatestSpecification(t *testing.T) {
	if os.Getenv("ETSI_LIVE_LATEST") == "" {
		t.Skip("set ETSI_LIVE_LATEST to run a live ETSI test")
	}
	ctx := context.Background()
	c := Client{}
	base := DefaultBase + "etsi_ts/123500_123599/123501/"
	dirs, err := c.listing(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	var version, pdf string
	for _, dir := range dirs {
		m := versionRE.FindStringSubmatch(dir)
		if m == nil {
			continue
		}
		files, e := c.listing(ctx, join(base, dir))
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range files {
			p := pdfRE.FindStringSubmatch(f)
			if p != nil && p[2] == "23501" && p[3] == m[1] && p[4] == m[2] && p[5] == m[3] {
				a, _ := strconv.Atoi(m[1])
				b, _ := strconv.Atoi(m[2])
				z, _ := strconv.Atoi(m[3])
				version = fmt.Sprintf("%d.%d.%d", a, b, z)
				pdf = join(join(base, dir), f)
				break
			}
		}
		if pdf != "" {
			break
		}
	}
	if pdf == "" {
		t.Fatal("no PDF in listing")
	}
	t.Logf("latest: %s %s", version, pdf)
	d, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		t.Fatal(err)
	}
	doc := Document{ID: "TS 23.501", Version: version, Series: "23", URL: pdf}
	if err = c.Import(ctx, d, doc); err != nil {
		t.Fatal(err)
	}
	listed, err := d.ListSpecs(ctx, "23", "23.501", 10, 0)
	if err != nil || len(listed.Specs) != 1 {
		t.Fatalf("list metadata: %+v, %v", listed, err)
	}
	if !strings.Contains(strings.ToLower(listed.Specs[0].Title), "architecture") || !strings.Contains(strings.Join(listed.Specs[0].Generations, ","), "5G") {
		t.Fatalf("incorrect PDF cover metadata: %+v", listed.Specs[0])
	}
	scope, err := d.GetSection(ctx, "TS 23.501", version, "1", false)
	if err != nil || len(scope) != 1 || !strings.Contains(strings.ToLower(scope[0].Content), "architecture") {
		t.Fatalf("scope: %+v, %v", scope, err)
	}
	results, err := d.Search(ctx, "architecture", nil, 10, 0)
	if err != nil || len(results.Results) == 0 {
		t.Fatalf("search: %+v, %v", results, err)
	}
}

func TestETSIRequestHeaders(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "Chrome/156") || r.Header.Get("Sec-Fetch-Mode") != "navigate" || r.Header.Get("Accept-Language") != "en,ru;q=0.9" {
			t.Errorf("missing ETSI headers: %v", r.Header)
		}
		fmt.Fprint(w, `<a href="123501/">spec</a>`)
	}))
	defer s.Close()
	if _, err := (Client{}).listing(context.Background(), s.URL+"/"); err != nil {
		t.Fatal(err)
	}
}

func TestCoverMetadataAndContent(t *testing.T) {
	text := `ETSI TS 132 299 V19.0.0 (2026-01)
TECHNICAL SPECIFICATION

Digital cellular telecommunications system (Phase 2+) (GSM);
Universal Mobile Telecommunications System (UMTS);
LTE;
5G;
Telecommunication management;
Charging management;
Diameter charging applications
(3GPP TS 32.299 version 19.0.0 Release 19)` + "\f" + `
Contents
1 Scope ........ 1
2 References ........ 2
3 Definitions ........ 3
4 Charging ........ 4
` + "\f" + `
1 Scope
Scope text.
2 References
Reference text.
3 Definitions
Definition text.
4 Charging
Table contents:
3GPP TS 23.502 defines the procedures.
1 Some numeric table data
Still part of charging.
                ETSI
`
	doc := Document{ID: "TS 32.299", Version: "19.0.0", Series: "32"}
	spec, sections, err := ParseText(doc, text)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Title != "Diameter charging applications" || spec.Area != "Telecommunication management; Charging management" || strings.Join(spec.Generations, ",") != "2G,3G,4G,5G" {
		t.Fatalf("metadata: %+v", spec)
	}
	if len(sections) != 4 || !strings.Contains(sections[3].Content, "1 Some numeric table data") || !strings.Contains(sections[3].Content, "3GPP TS 23.502 defines") || strings.Contains(sections[3].Content, "ETSI") {
		t.Fatalf("sections: %+v", sections)
	}
	d, err := db.OpenReadWrite(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		t.Fatal(err)
	}
	if err = d.InsertSpecWithSections(spec, sections); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListSpecs(context.Background(), "32", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Specs) != 1 || got.Specs[0].Title != spec.Title || got.Specs[0].Area != spec.Area || strings.Join(got.Specs[0].Generations, ",") != "2G,3G,4G,5G" {
		t.Fatalf("stored metadata: %+v", got.Specs)
	}
}

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"golang.org/x/net/html"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var assets embed.FS

const base = "https://www.etsi.org/deliver/"
const agent = "Mozilla/5.0 (X11; Linux x86_64; rv:135.0) Gecko/20100101 Firefox/135.0"

var digitsRE = regexp.MustCompile(`^\d{6}/$`)
var rangeRE = regexp.MustCompile(`^\d{6}_\d{6}/$`)
var versionDirRE = regexp.MustCompile(`^\d{2}\.\d{2}\.\d{2}_\d{2}/$`)

type crawler struct {
	client  tlsclient.HttpClient
	db      *sql.DB
	allowed map[string]bool
}

func (c *crawler) get(ctx context.Context, address string, limit int64) ([]byte, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", agent)
		req.Header.Set("Accept", "*/*")
		res, err := c.client.Do(req)
		if err == nil {
			b, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
			closeErr := res.Body.Close()
			switch {
			case e != nil:
				last = e
			case closeErr != nil:
				last = closeErr
			case len(b) > int(limit):
				return nil, fmt.Errorf("response exceeds %d bytes", limit)
			case res.StatusCode == 200:
				return b, nil
			default:
				last = fmt.Errorf("HTTP %d: %s", res.StatusCode, address)
				if res.StatusCode < 500 && res.StatusCode != 429 {
					return nil, last
				}
			}
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return nil, last
}
func (c *crawler) links(ctx context.Context, address string) ([]string, error) {
	b, err := c.get(ctx, address, 8<<20)
	if err != nil {
		return nil, err
	}
	root, err := html.Parse(strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(address)
	seen := map[string]bool{}
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key != "href" {
					continue
				}
				v, e := url.Parse(a.Val)
				if e != nil {
					continue
				}
				v = u.ResolveReference(v)
				// Only direct children of the current ETSI directory, no parent/external links.
				rest := strings.TrimPrefix(v.Path, u.Path)
				if v.Host == u.Host && v.Scheme == "https" && strings.HasPrefix(v.Path, u.Path) && rest != "" && !strings.Contains(strings.TrimSuffix(rest, "/"), "/") && !seen[v.String()] {
					seen[v.String()] = true
					out = append(out, v.String())
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(root)
	sort.Strings(out)
	return out, nil
}
func basename(s string) string {
	u, _ := url.Parse(s)
	return filepath.Base(strings.TrimSuffix(u.Path, "/")) + map[bool]string{true: "/"}[strings.HasSuffix(u.Path, "/")]
}
func (c *crawler) discover(ctx context.Context, root string) ([]string, error) {
	ranges, err := c.links(ctx, root)
	if err != nil {
		return nil, err
	}
	var docs []string
	for _, r := range ranges {
		if !rangeRE.MatchString(basename(r)) && !(strings.Contains(root, "etsi_gts/") && regexp.MustCompile(`^\d{2}/$`).MatchString(basename(r))) {
			continue
		}
		ls, e := c.links(ctx, r)
		if e != nil {
			return nil, e
		}
		for _, d := range ls {
			if digitsRE.MatchString(basename(d)) || strings.Contains(root, "etsi_gts/") && regexp.MustCompile(`^\d{4}/$`).MatchString(basename(d)) {
				name := strings.TrimSuffix(basename(d), "/")
				if len(c.allowed) > 0 && name >= "121000" && name <= "199999" {
					spec := name[1:3] + "." + name[3:]
					if !c.allowed[spec] {
						continue
					}
				}
				docs = append(docs, d)
			}
		}
	}
	return docs, nil
}
func (c *crawler) pdf(ctx context.Context, address string) (document, error) {
	b, err := c.get(ctx, address, 100<<20)
	if err != nil {
		return document{}, err
	}
	if !strings.HasPrefix(string(b[:min(5, len(b))]), "%PDF-") {
		return document{}, fmt.Errorf("not PDF: %s", address)
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	var existing string
	err = c.db.QueryRowContext(ctx, "SELECT sha256 FROM documents WHERE url=?", address).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return document{}, err
	}
	if existing == hash {
		return document{}, nil
	}
	tmp, err := os.MkdirTemp("", "etsi-pdf-")
	if err != nil {
		return document{}, err
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, "document.pdf")
	if err = os.WriteFile(path, b, 0600); err != nil {
		return document{}, err
	}
	cmd := exec.CommandContext(ctx, "pdftotext", "-layout", "-enc", "UTF-8", path, filepath.Join(tmp, "document.txt"))
	if out, e := cmd.CombinedOutput(); e != nil {
		return document{}, fmt.Errorf("pdftotext: %w: %.200s", e, out)
	}
	text, err := os.ReadFile(filepath.Join(tmp, "document.txt"))
	if err != nil {
		return document{}, err
	}
	d, err := parsePDF(string(text))
	if err != nil {
		return document{}, err
	}
	d.URL = address
	d.Hash = hash
	return d, nil
}
func (c *crawler) store(ctx context.Context, d document) error {
	if d.Spec == "" || len(c.allowed) > 0 && !c.allowed[d.Spec] {
		return nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Stable spec identity, newest published version only. Never replace newer data.
	var old string
	e := tx.QueryRowContext(ctx, "SELECT version FROM documents WHERE spec=?", d.Spec).Scan(&old)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	if e == nil && compareVersion(old, d.Version) > 0 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM documents WHERE spec=?", d.Spec); err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, "INSERT INTO documents(spec,family,series,title,version,url,sha256) VALUES(?,?,?,?,?,?,?)", d.Spec, d.Family, d.Series, d.Title, d.Version, d.URL, d.Hash)
	if err != nil {
		return err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return err
	}
	for _, s := range d.Sections {
		title, body := normalize(s.Title), normalize(s.Body)
		r, e := tx.ExecContext(ctx, "INSERT INTO sections(document_id,number,title,page) VALUES(?,?,?,?)", id, s.Number, s.Title, s.Page)
		if e != nil {
			return e
		}
		sectionID, e := r.LastInsertId()
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO section_text(section_id,body) VALUES(?,?)", sectionID, s.Body); e != nil {
			return e
		}
		counts := map[string][2]int{}
		for _, term := range strings.Fields(title) {
			v := counts[term]
			v[0]++
			counts[term] = v
		}
		for _, term := range strings.Fields(body) {
			v := counts[term]
			v[1]++
			counts[term] = v
		}
		stmt, e := tx.PrepareContext(ctx, "INSERT INTO postings(term,section_id,document_id,title_hits,body_hits) VALUES(?,?,?,?,?)")
		if e != nil {
			return e
		}
		for term, v := range counts {
			if _, e = stmt.ExecContext(ctx, term, sectionID, id, v[0], v[1]); e != nil {
				stmt.Close()
				return e
			}
		}
		if e = stmt.Close(); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func compareVersion(a, b string) int {
	var aa, bb [3]int
	_, _ = fmt.Sscanf(a, "%d.%d.%d", &aa[0], &aa[1], &aa[2])
	_, _ = fmt.Sscanf(b, "%d.%d.%d", &bb[0], &bb[1], &bb[2])
	for i := range aa {
		if aa[i] < bb[i] {
			return -1
		}
		if aa[i] > bb[i] {
			return 1
		}
	}
	return 0
}
func (c *crawler) index(ctx context.Context, address string) error {
	if strings.HasSuffix(strings.ToLower(address), ".pdf") {
		d, e := c.pdf(ctx, address)
		if e != nil {
			return e
		}
		return c.store(ctx, d)
	}
	versions, err := c.links(ctx, address)
	if err != nil {
		return err
	}
	var candidates []string
	for _, v := range versions {
		if versionDirRE.MatchString(basename(v)) {
			candidates = append(candidates, v)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(candidates)))
	for _, v := range candidates {
		files, e := c.links(ctx, v)
		if e != nil {
			return e
		}
		for _, f := range files {
			if strings.HasSuffix(strings.ToLower(f), ".pdf") {
				d, e := c.pdf(ctx, f)
				if e != nil {
					return e
				}
				return c.store(ctx, d)
			}
		}
	}
	return fmt.Errorf("no published PDF: %s", address)
}
func run() error {
	dbPath := flag.String("db", "data/explorer.db", "output SQLite database")
	source := flag.String("source", "", "ETSI specification directory or PDF (default: full 3GPP/GSM ETSI catalog)")
	mcp := flag.String("mcp-db", "", "optional existing MCP SQLite database: restrict to its specs")
	migrate := flag.Bool("migrate", false, "upgrade existing local database to schema v2, without downloading PDFs, then exit")
	workers := flag.Int("workers", 4, "parallel specification downloads (1..16)")
	flag.Parse()
	if *workers < 1 || *workers > 16 {
		return fmt.Errorf("workers must be 1..16")
	}
	if _, e := exec.LookPath("pdftotext"); e != nil && !*migrate {
		return fmt.Errorf("install poppler-utils: %w", e)
	}
	if *migrate {
		if _, err := os.Stat(*dbPath); err != nil {
			return fmt.Errorf("migration requires an existing database at %s: %w", *dbPath, err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if *migrate {
		var version, exists int
		if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sections'").Scan(&exists); err != nil {
			return err
		}
		if version < 2 && exists > 0 {
			backup := fmt.Sprintf("%s.pre-v2-%d.db", *dbPath, time.Now().UnixNano())
			if _, err = db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
				return fmt.Errorf("backup before migration: %w", err)
			}
			log.Printf("Consistent pre-migration backup: %s", backup)
		}
	}
	if err = ensureSchema(ctx, db, *migrate); err != nil {
		return err
	}
	if *migrate {
		log.Print("Database schema v2 ready; no downloads performed")
		return nil
	}

	opts := []tlsclient.HttpClientOption{tlsclient.WithClientProfile(profiles.Firefox_135), tlsclient.WithTimeoutSeconds(120)}
	proxy := os.Getenv("HTTPS_PROXY")
	if proxy == "" {
		proxy = os.Getenv("https_proxy")
	}
	if proxy != "" {
		opts = append(opts, tlsclient.WithProxyUrl(proxy))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), opts...)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	c := crawler{client: client, db: db, allowed: map[string]bool{}}
	if *mcp != "" {
		abs, e := filepath.Abs(*mcp)
		if e != nil {
			return e
		}
		u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro"}
		other, e := sql.Open("sqlite", u.String())
		if e != nil {
			return e
		}
		defer other.Close()
		rows, e := other.QueryContext(ctx, "SELECT id FROM specs")
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			m := regexp.MustCompile(`\d{2}\.\d{2,3}(?:-\d+)?`).FindString(id)
			if m != "" {
				c.allowed[m] = true
			}
		}
		if e = rows.Err(); e != nil {
			return e
		}
		if len(c.allowed) == 0 {
			return fmt.Errorf("MCP catalog is empty")
		}
	}
	var jobs []string
	if *source != "" {
		u, e := url.Parse(*source)
		if e != nil || u.Host != "www.etsi.org" || u.Scheme != "https" || !strings.HasPrefix(u.Path, "/deliver/") {
			return fmt.Errorf("source must be an https://www.etsi.org/deliver/ URL")
		}
		jobs = []string{*source}
	} else {
		for _, kind := range []string{"etsi_ts/", "etsi_tr/", "etsi_en/", "etsi_etr/", "etsi_i_ets/", "etsi_gts/"} {
			ls, e := c.discover(ctx, base+kind)
			if e != nil {
				return e
			}
			jobs = append(jobs, ls...)
		}
	}
	log.Printf("Catalog: %d specification directories; Firefox 135 UA + uTLS", len(jobs))
	queue := make(chan string)
	var wg sync.WaitGroup
	var failures atomic.Int64
	for range *workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for address := range queue {
				if e := c.index(ctx, address); e != nil {
					if errors.Is(e, errNotSpec) {
						log.Printf("SKIP %s: unrelated ETSI document", address)
						continue
					}
					failures.Add(1)
					log.Printf("FAILED %s: %v", address, e)
				} else {
					log.Printf("OK %s", address)
				}
			}
		}()
	}
	for _, job := range jobs {
		select {
		case queue <- job:
		case <-ctx.Done():
			close(queue)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(queue)
	wg.Wait()
	if _, err = db.ExecContext(ctx, "PRAGMA optimize"); err != nil {
		return err
	}
	if len(c.allowed) > 0 {
		rows, e := db.QueryContext(ctx, "SELECT spec FROM documents")
		if e != nil {
			return e
		}
		found := map[string]bool{}
		for rows.Next() {
			var spec string
			if e = rows.Scan(&spec); e != nil {
				rows.Close()
				return e
			}
			found[spec] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		var missing []string
		for spec := range c.allowed {
			if !found[spec] {
				missing = append(missing, spec)
			}
		}
		sort.Strings(missing)
		report := *dbPath + ".missing.txt"
		if e = os.WriteFile(report, []byte(strings.Join(missing, "\n")), 0644); e != nil {
			return e
		}
		if len(missing) > 0 {
			failures.Add(int64(len(missing)))
			log.Printf("MCP catalog: %d specifications missing; see %s", len(missing), report)
		}
	}
	if failures.Load() > 0 {
		return fmt.Errorf("%d sources failed; successful documents committed; rerun to retry", failures.Load())
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

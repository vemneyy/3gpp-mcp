// Package etsi discovers and imports 3GPP deliverables published by ETSI.
package etsi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/higebu/3gpp-mcp/internal/db"
	"golang.org/x/net/html"
)

const DefaultBase = "https://www.etsi.org/deliver/"
const maxListing = 16 << 20
const maxPDF = 200 << 20

var groupRE = regexp.MustCompile(`^1[0-9]{5}_1[0-9]{5}/$`)
var specRE = regexp.MustCompile(`^1[0-9]{5}(?:[0-9]{2})?/$`)
var versionRE = regexp.MustCompile(`^([0-9]{2})\.([0-9]{2})\.([0-9]{2})_[0-9]+/$`)
var pdfRE = regexp.MustCompile(`(?i)^(ts|tr)_1([0-9]{5}(?:[0-9]{2})?)v([0-9]{2})([0-9]{2})([0-9]{2})p\.pdf$`)
var headingRE = regexp.MustCompile(`^\s*((?:[0-9]+[A-Za-z]?(?:\.[0-9]+[A-Za-z]?)*|[A-Z](?:\.[0-9]+[A-Za-z]?)+))\s+([^\n]{2,180}?)\s*$`)
var coverRE = regexp.MustCompile(`(?i)3GPP\s+(TS|TR)\s+([0-9]{2}\.[0-9]{2,3}(?:-[0-9]+)?)\s+version\s+([0-9]+\.[0-9]+\.[0-9]+)`)

// ErrNot3GPP marks an ETSI publication whose cover does not identify it as a 3GPP specification.
var ErrNot3GPP = errors.New("ETSI PDF is not identified as a 3GPP specification")

var pageRE = regexp.MustCompile(`^\s*(?:ETSI\s*|3GPP (?:TS|TR)\s+.*\bversion\b.*\bETSI\s+(?:TS|TR)\s+.*|ETSI\s+(?:TS|TR)\s+[0-9].*\bV[0-9].*)\s*$`)
var tocLineRE = regexp.MustCompile(`^\s*((?:[0-9]+[A-Za-z]?(?:\.[0-9]+[A-Za-z]?)*|[A-Z](?:\.[0-9]+[A-Za-z]?)+))\s+(.+?)\s*\.{3,}\s*[0-9]+\s*$`)
var coverDesignationRE = regexp.MustCompile(`(?i)\b3GPP\s+(?:TS|TR)\b`)
var generationRE = regexp.MustCompile(`(?i)\b(GSM|UMTS|LTE|2G|3G|4G|5G|6G)\b`)

// coverMetadata separates the ETSI cover's semicolon-delimited technology,
// subject area and title. Only explicitly named generations are tagged.
func coverMetadata(text, fallback string) (string, string, []string) {
	cover, _, _ := strings.Cut(text, "\f")
	if match := coverRE.FindStringIndex(cover); match != nil {
		cover = cover[:match[0]]
	}
	lines := strings.Split(cover, "\n")
	var parts []string
	started := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "TECHNICAL SPECIFICATION") || strings.HasPrefix(line, "TECHNICAL REPORT") {
			started = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "ETSI ") {
			continue
		}
		if started {
			parts = append(parts, line)
		}
	}
	if len(parts) == 0 {
		return fallback, "", nil
	}
	joined := strings.TrimSpace(strings.TrimSuffix(strings.Join(parts, " "), "("))
	var generations []string
	seen := map[string]bool{}
	for _, match := range generationRE.FindAllStringSubmatch(joined, -1) {
		label := strings.ToUpper(match[1])
		switch label {
		case "GSM":
			label = "2G"
		case "UMTS":
			label = "3G"
		case "LTE":
			label = "4G"
		}
		if !seen[label] {
			seen[label] = true
			generations = append(generations, label)
		}
	}
	fields := strings.Split(joined, ";")
	var subject []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if generationRE.MatchString(f) && (strings.Contains(strings.ToUpper(f), "GSM") || strings.Contains(strings.ToUpper(f), "UMTS") || strings.EqualFold(f, "LTE") || strings.EqualFold(f, "2G") || strings.EqualFold(f, "3G") || strings.EqualFold(f, "4G") || strings.EqualFold(f, "5G") || strings.EqualFold(f, "6G")) {
			continue
		}
		subject = append(subject, f)
	}
	if len(subject) == 0 {
		return fallback, "", generations
	}
	return subject[len(subject)-1], strings.Join(subject[:len(subject)-1], "; "), generations
}

type Document struct{ ID, Version, Series, URL string }
type Client struct {
	Progress func(string)
	Base     string
	HTTP     *http.Client
}

func (c Client) progress(format string, args ...any) {
	if c.Progress != nil {
		c.Progress(fmt.Sprintf(format, args...))
	}
}

type progressWriter struct {
	out      io.Writer
	total, n int64
	last     time.Time
	report   func(int64, int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.out.Write(b)
	p.n += int64(n)
	if time.Since(p.last) >= 5*time.Second {
		p.report(p.n, p.total)
		p.last = time.Now()
	}
	return n, err
}

func (c Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultClient
}

// setETSIHeaders supplies the browser navigation headers required by ETSI's
// delivery endpoint in some network environments. Go handles gzip itself.
func setETSIHeaders(req *http.Request) {
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/jxl,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Accept-Language", "en,ru;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("DNT", "1")
	req.Header.Set("Sec-CH-UA", `"Not:A-Brand";v="8", "Chromium";v="156"`)
	req.Header.Set("Sec-CH-UA-Mobile", "?0")
	req.Header.Set("Sec-CH-UA-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36")
}

func (c Client) listing(ctx context.Context, address string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	setETSIHeaders(req)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("listing %s: HTTP %s", address, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxListing+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxListing {
		return nil, fmt.Errorf("listing too large: %s", address)
	}
	root, err := html.Parse(strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					u, e := url.Parse(a.Val)
					if e == nil && u.RawQuery == "" && u.Fragment == "" {
						resolved := resp.Request.URL.ResolveReference(u)
						prefix := strings.TrimRight(resp.Request.URL.Path, "/") + "/"
						if resolved.Scheme == resp.Request.URL.Scheme && resolved.Host == resp.Request.URL.Host && strings.HasPrefix(resolved.Path, prefix) {
							v := strings.TrimPrefix(resolved.Path, prefix)
							if v != "" && !strings.Contains(strings.TrimSuffix(v, "/"), "/") && v != "../" {
								out = append(out, v)
							}
						}
					}
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(root)
	if len(out) == 0 {
		return nil, fmt.Errorf("listing %s contains no links (access denied or invalid directory listing)", address)
	}
	return out, nil
}
func join(base, elem string) string { return strings.TrimRight(base, "/") + "/" + elem }

// Discover traverses every 3GPP-numbered ETSI TS and TR directory and selects
// the latest published version per specification. Any failed listing fails the
// run, so an incomplete catalogue cannot silently look complete.
func (c Client) Discover(ctx context.Context) ([]Document, error) {
	var docs []Document
	err := c.Stream(ctx, 8, func(doc Document) error { docs = append(docs, doc); return nil })
	if err != nil {
		return nil, err
	}
	// Keep the collecting API deterministic; the indexer uses Stream directly.
	order := func(id string) string {
		if strings.HasPrefix(id, "TS ") {
			return "0" + id
		}
		if strings.HasPrefix(id, "TR ") {
			return "1" + id
		}
		return "2" + id
	}
	sort.Slice(docs, func(i, j int) bool { return order(docs[i].ID) < order(docs[j].ID) })
	return docs, nil
}

// discoverMobileEN selects the cellular radio equipment families EN 301 908 and EN 301 502.
func (c Client) discoverMobileEN(ctx context.Context, root string) ([]Document, error) {
	groups, err := c.listing(ctx, root)
	if err != nil {
		return nil, err
	}
	var docs []Document
	fileRE := regexp.MustCompile(`(?i)^en_([0-9]{6}(?:[0-9]{2})?)v([0-9]{2})([0-9]{2})([0-9]{2})p\.pdf$`)
	for _, g := range groups {
		if g != "301900_301999/" && g != "301500_301599/" {
			continue
		}
		c.progress("Discovery: etsi_en/%s", g)
		specs, e := c.listing(ctx, join(root, g))
		if e != nil {
			return nil, e
		}
		for _, sp := range specs {
			num := strings.TrimSuffix(sp, "/")
			if !strings.HasPrefix(num, "301908") && !strings.HasPrefix(num, "301502") {
				continue
			}
			address := join(join(root, g), sp)
			vs, e := c.listing(ctx, address)
			if e != nil {
				return nil, e
			}
			sort.Sort(sort.Reverse(sort.StringSlice(vs)))
			for _, v := range vs {
				vm := versionRE.FindStringSubmatch(v)
				if vm == nil {
					continue
				}
				files, e := c.listing(ctx, join(address, v))
				if e != nil {
					return nil, e
				}
				found := false
				for _, f := range files {
					m := fileRE.FindStringSubmatch(f)
					if m == nil || m[1] != num || m[2] != vm[1] || m[3] != vm[2] || m[4] != vm[3] {
						continue
					}
					a, _ := strconv.Atoi(m[2])
					b, _ := strconv.Atoi(m[3])
					z, _ := strconv.Atoi(m[4])
					docs = append(docs, Document{ID: "ETSI EN " + num, Version: fmt.Sprintf("%d.%d.%d", a, b, z), URL: join(join(address, v), f)})
					found = true
					break
				}
				if found {
					break
				}
			}
		}
	}
	return docs, nil
}

// discoverGTS reads historical GSM publications, whose layout differs from TS/TR.
func (c Client) discoverGTS(ctx context.Context, root string) ([]Document, error) {
	groups, err := c.listing(ctx, root)
	if err != nil {
		return nil, err
	}
	var docs []Document
	groupRE := regexp.MustCompile(`^[0-9]{2}/$`)
	docRE := regexp.MustCompile(`^[0-9]{4,8}/$`)
	fileRE := regexp.MustCompile(`(?i)^gsmts_([0-9]+)v([0-9]{2})([0-9]{2})([0-9]{2})p\.pdf$`)
	for _, group := range groups {
		if !groupRE.MatchString(group) {
			continue
		}
		c.progress("Discovery: etsi_gts/%s", group)
		specs, e := c.listing(ctx, join(root, group))
		if e != nil {
			return nil, e
		}
		for _, spec := range specs {
			if !docRE.MatchString(spec) {
				continue
			}
			sp := join(join(root, group), spec)
			versions, e := c.listing(ctx, sp)
			if e != nil {
				return nil, e
			}
			sort.Sort(sort.Reverse(sort.StringSlice(versions)))
			for _, v := range versions {
				vm := versionRE.FindStringSubmatch(v)
				if vm == nil {
					continue
				}
				files, e := c.listing(ctx, join(sp, v))
				if e != nil {
					return nil, e
				}
				found := false
				for _, f := range files {
					m := fileRE.FindStringSubmatch(f)
					if m == nil || m[1]+"/" != spec || m[2] != vm[1] || m[3] != vm[2] || m[4] != vm[3] {
						continue
					}
					a, _ := strconv.Atoi(m[2])
					b, _ := strconv.Atoi(m[3])
					z, _ := strconv.Atoi(m[4])
					number := m[1][:2] + "." + m[1][2:]
					docs = append(docs, Document{ID: "GSM " + number, Version: fmt.Sprintf("%d.%d.%d", a, b, z), Series: m[1][:2], URL: join(join(sp, v), f)})
					found = true
					break
				}
				if found {
					break
				}
			}
		}
	}
	return docs, nil
}

// headingMatch avoids invoking the regular expression on ordinary prose lines.
func headingMatch(line string) []string {
	trimmed := strings.TrimLeft(line, " \t\r\n\f")
	if trimmed == "" || ((trimmed[0] < '0' || trimmed[0] > '9') && (trimmed[0] < 'A' || trimmed[0] > 'Z')) {
		return nil
	}
	return headingRE.FindStringSubmatch(line)
}

// ParseText splits pdftotext output into numbered clauses. The table of
// contents is skipped using the last Scope heading before clause 2.
func ParseText(doc Document, text string) (db.Spec, []db.Section, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\f", "\n"), "\n")
	start := -1
	for i, l := range lines {
		m := headingMatch(l)
		if m != nil && m[1] == "1" && strings.EqualFold(strings.TrimSpace(m[2]), "Scope") {
			start = i
		} else if start >= 0 && m != nil && m[1] == "2" {
			break
		}
	}
	if start < 0 {
		return db.Spec{}, nil, fmt.Errorf("%s: cannot find Scope heading in extracted PDF text", doc.ID)
	}
	// ETSI's table of contents gives clause numbers and headings. Use it
	// when present to avoid treating numeric table rows as section headings.
	toc := make(map[string]string)
	for _, line := range lines[:start] {
		if m := tocLineRE.FindStringSubmatch(line); m != nil {
			toc[m[1]] = strings.Join(strings.Fields(m[2]), " ")
		}
	}
	var sections []db.Section
	var body []string
	flush := func() {
		if len(sections) > 0 {
			sections[len(sections)-1].Content = strings.TrimSpace(strings.Join(body, "\n"))
		}
		body = nil
	}
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if (trimmed == "ETSI" || strings.HasPrefix(trimmed, "ETSI ") || strings.HasPrefix(trimmed, "3GPP TS ") || strings.HasPrefix(trimmed, "3GPP TR ")) && pageRE.MatchString(line) {
			continue
		}
		m := headingMatch(line)
		if m != nil && len(toc) >= 4 {
			title, exists := toc[m[1]]
			candidate := strings.Join(strings.Fields(m[2]), " ")
			if !exists || (candidate != title && (len(candidate) < 12 || !strings.HasPrefix(title, candidate)) && (len(title) < 12 || !strings.HasPrefix(candidate, title))) {
				m = nil
			}
		}
		if m != nil && !strings.Contains(m[2], ".....") {
			num := m[1]
			if len(sections) > 0 && num == sections[len(sections)-1].Number {
				body = append(body, line)
				continue
			}
			flush()
			parent := ""
			if i := strings.LastIndex(num, "."); i >= 0 {
				parent = num[:i]
			}
			sections = append(sections, db.Section{SpecID: doc.ID, Version: doc.Version, Number: num, Title: strings.TrimSpace(m[2]), Level: strings.Count(num, ".") + 1, ParentNumber: parent})
			continue
		}
		body = append(body, line)
	}
	flush()
	if len(sections) < 2 {
		return db.Spec{}, nil, fmt.Errorf("%s: too few sections extracted", doc.ID)
	}
	major, _, _ := strings.Cut(doc.Version, ".")
	title, area, generations := coverMetadata(text, doc.ID)
	return db.Spec{ID: doc.ID, Title: title, Area: area, Generations: generations, PDFMetadata: true, Version: doc.Version, Release: major, Series: doc.Series}, sections, nil
}

// Import downloads one PDF to a temporary file and extracts text with Poppler.
func (c Client) Import(ctx context.Context, d *db.DB, doc Document) error {
	req, err := http.NewRequestWithContext(ctx, "GET", doc.URL, nil)
	if err != nil {
		return err
	}
	setETSIHeaders(req)
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", doc.URL, resp.Status)
	}
	f, err := os.CreateTemp("", "3gpp-*.pdf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	c.progress("%s v%s: downloading PDF", doc.ID, doc.Version)
	downloadStart := time.Now()
	writer := &progressWriter{out: f, total: resp.ContentLength, last: time.Now(), report: func(n, total int64) {
		if total > 0 {
			c.progress("%s v%s: downloaded %.1f / %.1f MiB (%.0f%%), %.2f MiB/s", doc.ID, doc.Version, float64(n)/(1<<20), float64(total)/(1<<20), 100*float64(n)/float64(total), float64(n)/(1<<20)/time.Since(downloadStart).Seconds())
		} else {
			c.progress("%s v%s: downloaded %.1f MiB", doc.ID, doc.Version, float64(n)/(1<<20))
		}
	}}
	n, err := io.Copy(writer, io.LimitReader(resp.Body, maxPDF+1))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n > maxPDF {
		return fmt.Errorf("PDF exceeds size limit: %s", doc.URL)
	}
	signature, err := os.Open(f.Name())
	if err != nil {
		return err
	}
	header := make([]byte, 5)
	_, err = io.ReadFull(signature, header)
	_ = signature.Close()
	if err != nil || string(header) != "%PDF-" {
		return fmt.Errorf("%s: response is not a PDF", doc.URL)
	}
	c.progress("%s v%s: downloaded %.1f MiB; extracting PDF text", doc.ID, doc.Version, float64(n)/(1<<20))
	out, err := exec.CommandContext(ctx, "pdftotext", "-layout", f.Name(), "-").Output()
	if err != nil {
		return fmt.Errorf("extract %s: %w", path.Base(doc.URL), err)
	}
	// ETSI also publishes unrelated standards in the same number ranges.
	// Identify the source from the cover, not merely from its directory number.
	text := string(out)
	cover := text
	if i := strings.IndexByte(cover, '\f'); i >= 0 {
		cover = cover[:i]
	}
	match := coverRE.FindStringSubmatch(cover)
	if match == nil && len(strings.TrimSpace(cover)) < 50 {
		return fmt.Errorf("%s: empty or unreadable PDF cover", doc.URL)
	}
	if match != nil {
		// The 3GPP number on older ETSI covers differs from the ETSI number.
		// ETSI's edition and the 3GPP version also need not be identical.
		doc.ID = strings.ToUpper(match[1]) + " " + match[2]
	} else if coverDesignationRE.MatchString(cover) {
		return fmt.Errorf("%s: cannot reliably identify 3GPP publication from PDF cover", doc.URL)
	} else if match := pdfRE.FindStringSubmatch(path.Base(doc.URL)); match != nil {
		// The path names an ETSI document, not a dotted 3GPP number.
		number := "1" + match[2][:5]
		if len(match[2]) > 5 {
			number += "-" + match[2][5:]
		}
		doc.ID = "ETSI " + strings.ToUpper(match[1]) + " " + number
	} else if !strings.HasPrefix(doc.ID, "ETSI ") && !strings.HasPrefix(doc.ID, "GSM ") {
		doc.ID = "ETSI " + doc.ID
	}

	spec, sections, err := ParseText(doc, text)
	if err != nil {
		return err
	}
	c.progress("%s v%s: indexing %d sections in SQLite", doc.ID, doc.Version, len(sections))
	return d.InsertSpecWithSections(spec, sections)
}

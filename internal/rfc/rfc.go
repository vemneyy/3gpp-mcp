// Package rfc indexes RFC Editor text documents in the shared searchable database.
package rfc

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/higebu/3gpp-mcp/internal/db"
)

const DefaultBase = "https://www.rfc-editor.org/"

type Entry struct {
	ID    string `xml:"doc-id"`
	Title string `xml:"title"`
}
type catalog struct {
	Entries []Entry `xml:"rfc-entry"`
}
type Client struct {
	Base     string
	HTTP     *http.Client
	Progress func(string)
}

func (c Client) base() string {
	if c.Base == "" {
		return DefaultBase
	}
	return strings.TrimRight(c.Base, "/") + "/"
}
func (c Client) get(ctx context.Context, path string, limit int64) ([]byte, error) {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.base()+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %s", path, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds size limit", path)
	}
	return data, nil
}

var idRE = regexp.MustCompile(`^RFC([0-9]+)$`)

func (c Client) Discover(ctx context.Context) ([]Entry, error) {
	data, err := c.get(ctx, "rfc-index.xml", 64<<20)
	if err != nil {
		return nil, err
	}
	var cat catalog
	if err = xml.Unmarshal(data, &cat); err != nil {
		return nil, fmt.Errorf("RFC index: %w", err)
	}
	var entries []Entry
	for _, e := range cat.Entries {
		if idRE.MatchString(e.ID) {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("RFC index contains no entries")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return entries, nil
}

var heading = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)*)\.?(?:[ \t]{2,}|\t)([^\n]{2,160})$`)

// Parse preserves the full RFC text even when the document uses an unusual layout.
func Parse(e Entry, text string) (db.Spec, []db.Section, error) {
	m := idRE.FindStringSubmatch(e.ID)
	if m == nil {
		return db.Spec{}, nil, fmt.Errorf("invalid RFC id %q", e.ID)
	}
	n, _ := strconv.Atoi(m[1])
	id := fmt.Sprintf("RFC %d", n)
	text = strings.ReplaceAll(text, "\f", "\n")
	lines := strings.Split(text, "\n")
	start := 0
	// Skip a table of contents by beginning at the last section 1 before section 2.
	for i, l := range lines {
		h := heading.FindStringSubmatch(l)
		if h != nil && h[1] == "1" && !strings.Contains(h[2], "...") {
			start = i
		} else if h != nil && h[1] == "2" && start > 0 {
			break
		}
	}
	sections := []db.Section{{SpecID: id, Number: "preamble", Title: "Preamble"}}
	var body []string
	flush := func() { sections[len(sections)-1].Content = strings.TrimSpace(strings.Join(body, "\n")); body = nil }
	body = append(body, lines[:start]...)
	for _, line := range lines[start:] {
		h := heading.FindStringSubmatch(line)
		if h != nil && !strings.Contains(h[2], "...") {
			flush()
			num := h[1]
			parent := ""
			if p := strings.LastIndex(num, "."); p >= 0 {
				parent = num[:p]
			}
			sections = append(sections, db.Section{SpecID: id, Number: num, Title: strings.TrimSpace(h[2]), Level: strings.Count(num, ".") + 1, ParentNumber: parent})
		} else {
			body = append(body, line)
		}
	}
	flush()
	if strings.TrimSpace(text) == "" {
		return db.Spec{}, nil, fmt.Errorf("%s: empty document", id)
	}
	return db.Spec{ID: id, Version: "published", Title: strings.TrimSpace(e.Title), Series: "RFC"}, sections, nil
}
func (c Client) Import(ctx context.Context, d *db.DB, e Entry) error {
	m := idRE.FindStringSubmatch(e.ID)
	if m == nil {
		return fmt.Errorf("invalid RFC id %q", e.ID)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return err
	}
	data, err := c.get(ctx, fmt.Sprintf("rfc/rfc%d.txt", n), 32<<20)
	if err != nil {
		return err
	}
	spec, sections, err := Parse(e, string(data))
	if err != nil {
		return err
	}
	return d.InsertSpecWithSections(spec, sections)
}

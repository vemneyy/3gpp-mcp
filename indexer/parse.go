package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type section struct {
	Number, Title, Body string
	Page                int
}
type document struct {
	Spec, Family, Series, Title, Version, URL, Hash string
	Sections                                        []section
}

var errNotSpec = errors.New("not a 3GPP/GSM specification")
var specRE = regexp.MustCompile(`(?i)(3GPP|GSM)\s+(?:(?:TS|TR)\s+)?(\d{2}\.\d{2,3}(?:-\d+)?)`)
var versionRE = regexp.MustCompile(`(?i)\bV(\d+\.\d+\.\d+)`)
var tocRE = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)*|[A-Z](?:\.[0-9]+)*|Annex\s+[A-Z])\s+(.+?)\s*(?:\.{2,}|\s{3,})\s*([0-9]+)\s*$`)
var headingRE = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)*|[A-Z](?:\.[0-9]+)*|Annex\s+[A-Z])\s+(.+?)\s*$`)
var footerRE = regexp.MustCompile(`^\s*(?:ETSI|[0-9]+|ETSI\s+(?:TS|TR).*)\s*$`)

// Page is the printed page from the TOC. Physical PDF index is printed+1.
// Body starts at the matching heading, not at the beginning of its page.
func parsePDF(text string) (document, error) {
	pages := strings.Split(text, "\f")
	cover := strings.Join(pages[:min(len(pages), 3)], "\n")
	m := specRE.FindStringSubmatch(cover)
	if m == nil {
		return document{}, errNotSpec
	}
	d := document{Spec: m[2], Family: "3gpp", Series: strings.Split(m[2], ".")[0]}
	if strings.EqualFold(m[1], "GSM") || d.Series < "21" {
		d.Family = "gsm"
	}
	if v := versionRE.FindStringSubmatch(cover); v != nil {
		d.Version = v[1]
	}
	// Cover title lies before the 3GPP identity, below the ETSI/version header.
	for _, line := range strings.Split(pages[0], "\n") {
		s := strings.TrimSpace(line)
		if s == "TECHNICAL SPECIFICATION" || s == "TECHNICAL REPORT" || s == "" || strings.Contains(s, "ETSI") || strings.Contains(s, "www.") || versionRE.MatchString(s) {
			continue
		}
		if specRE.MatchString(s) {
			break
		}
		d.Title += s + " "
	}
	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" {
		d.Title = d.Spec
	}
	seen := map[string]bool{}
	// Only read entries from Contents pages, never dotted body/table lines.
	inTOC := false
	for pi, p := range pages {
		if pi > 60 {
			break
		}
		if regexp.MustCompile(`(?im)^\s*(Contents|Table of contents)\s*$`).MatchString(p) {
			inTOC = true
		}
		if !inTOC {
			continue
		}
		found := false
		for _, line := range strings.Split(p, "\n") {
			t := tocRE.FindStringSubmatch(line)
			if t == nil {
				continue
			}
			n, _ := strconv.Atoi(t[3])
			if n < 1 || n >= len(pages) || seen[t[1]] {
				continue
			}
			seen[t[1]] = true
			found = true
			d.Sections = append(d.Sections, section{Number: t[1], Title: strings.TrimSpace(t[2]), Page: n})
		}
		if !found && len(d.Sections) > 0 {
			break
		}
	}
	if len(d.Sections) == 0 {
		return document{}, fmt.Errorf("%s: no numbered TOC entries; refusing page guesses", d.Spec)
	}
	type position struct{ page, line int }
	positions := make([]position, len(d.Sections))
	lines := make([][]string, len(pages))
	for i, p := range pages {
		lines[i] = strings.Split(p, "\n")
	}
	for i, s := range d.Sections {
		positions[i] = position{-1, -1}
		// Physical page is zero-based s.Page (printed+1 in one-based PDF viewer).
		// Some published TOCs contain stale pages. Verify the heading and
		// recover within two printed pages; retain the verified printed page.
		for _, pi := range []int{s.Page, s.Page - 1, s.Page + 1, s.Page - 2, s.Page + 2} {
			if pi < 1 || pi >= len(lines) {
				continue
			}
			for li, line := range lines[pi] {
				h := headingRE.FindStringSubmatch(line)
				if h != nil && h[1] == s.Number && strings.HasPrefix(normalize(s.Title), normalize(h[2])) {
					positions[i] = position{pi, li}
					d.Sections[i].Page = pi
					break
				}
			}
			if positions[i].page >= 0 {
				break
			}
		}
		if positions[i].page < 0 {
			return document{}, fmt.Errorf("%s section %s: TOC page %d does not contain heading", d.Spec, s.Number, s.Page)
		}
	}
	for i := range d.Sections {
		start := positions[i]
		end := position{len(pages) - 1, len(lines[len(pages)-1])}
		if i+1 < len(positions) {
			end = positions[i+1]
		}
		if end.page < start.page || end.page == start.page && end.line < start.line {
			return document{}, fmt.Errorf("non-monotonic TOC")
		}
		var body strings.Builder
		for pi := start.page; pi <= end.page; pi++ {
			lo, hi := 0, len(lines[pi])
			if pi == start.page {
				lo = start.line + 1
			}
			if pi == end.page {
				hi = end.line
			}
			for _, line := range lines[pi][lo:hi] {
				if !footerRE.MatchString(line) {
					body.WriteString(strings.TrimSpace(line))
					body.WriteByte('\n')
				}
			}
		}
		d.Sections[i].Body = strings.TrimSpace(body.String())
	}
	return d, nil
}

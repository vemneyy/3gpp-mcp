package main

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
)

var etsiNumberRE = regexp.MustCompile(`/([0-9]{6})/`)
var definitionRE = regexp.MustCompile(`^([^:]{2,100}):\s+(.+)$`)
var abbreviationRE = regexp.MustCompile(`^([A-Z0-9][A-Za-z0-9/() _-]{1,30}?)\s{2,}(.+)$`)
var plainAbbreviationRE = regexp.MustCompile(`^([A-Z][A-Z0-9-]{1,20})\s+([A-Za-z].+)$`)

func sectionKind(title string) string {
	n := normalize(title)
	for _, x := range []string{"change history", "changehistory", "revision history", "version history", "history of changes"} {
		if strings.Contains(n, x) {
			return "history"
		}
	}
	if n == "references" || n == "normative references" || n == "bibliography" {
		return "references"
	}
	if n == "definitions" || n == "abbreviations" || n == "terms and definitions" || n == "definitions and abbreviations" || n == "definitions symbols and abbreviations" || n == "terms definitions and abbreviations" || n == "symbols and abbreviations" {
		return "glossary"
	}
	return "content"
}

type glossaryEntry struct{ Term, Definition, Kind string }

// Extract only explicit term/definition pairs inside marked definitions and
// abbreviations sections. No generated definitions and no guessed expansions.
func glossaryEntries(title, body string) []glossaryEntry {
	if sectionKind(title) != "glossary" {
		return nil
	}
	abbreviations := strings.Contains(normalize(title), "abbreviation")
	var out []glossaryEntry
	var current *glossaryEntry
	flush := func() {
		if current != nil && len(current.Definition) >= 3 {
			out = append(out, *current)
		}
		current = nil
	}
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "NOTE") || strings.HasPrefix(line, "3GPP ") || strings.HasPrefix(line, "ETSI") || strings.HasPrefix(line, "For the purposes") {
			flush()
			continue
		}
		var m []string
		kind := "definition"
		if abbreviations {
			m = abbreviationRE.FindStringSubmatch(line)
			if m == nil {
				m = plainAbbreviationRE.FindStringSubmatch(line)
			}
			kind = "abbreviation"
		}
		if m == nil {
			m = definitionRE.FindStringSubmatch(line)
			kind = "definition"
		}
		if m != nil {
			flush()
			term := strings.TrimSpace(m[1])
			definition := strings.TrimSpace(m[2])
			if len(strings.Fields(term)) <= 12 && !strings.ContainsAny(term, "[]") {
				current = &glossaryEntry{term, definition, kind}
			}
		} else if current != nil && len(current.Definition)+len(line) < 1600 {
			current.Definition += " " + line
		}
	}
	flush()
	return out
}

func enrichDocument(ctx context.Context, tx *sql.Tx, id int64) error {
	var spec, title, address string
	if e := tx.QueryRowContext(ctx, "SELECT spec,title,url FROM documents WHERE id=?", id).Scan(&spec, &title, &address); e != nil {
		return e
	}
	etsi := ""
	if m := etsiNumberRE.FindStringSubmatch(address); m != nil {
		etsi = m[1]
	}
	if _, e := tx.ExecContext(ctx, "INSERT INTO document_search(document_id,title_norm,spec_compact,etsi_number) VALUES(?,?,?,?)", id, normalize(title), strings.ReplaceAll(spec, ".", ""), etsi); e != nil {
		return e
	}
	counts := map[string]int{}
	for _, w := range strings.Fields(normalize(title)) {
		counts[w]++
	}
	for w, n := range counts {
		if _, e := tx.ExecContext(ctx, "INSERT INTO document_postings(term,document_id,hits) VALUES(?,?,?)", w, id, n); e != nil {
			return e
		}
	}
	rows, e := tx.QueryContext(ctx, "SELECT s.id,s.title,t.body FROM sections s JOIN section_text t ON t.section_id=s.id WHERE s.document_id=? ORDER BY s.id", id)
	if e != nil {
		return e
	}
	type entry struct {
		ID          int64
		Title, Body string
	}
	var entries []entry
	for rows.Next() {
		var s entry
		if e = rows.Scan(&s.ID, &s.Title, &s.Body); e != nil {
			rows.Close()
			return e
		}
		entries = append(entries, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, s := range entries {
		if _, e = tx.ExecContext(ctx, "INSERT INTO section_features(section_id,kind,body_length,title_length) VALUES(?,?,?,?)", s.ID, sectionKind(s.Title), len(strings.Fields(normalize(s.Body))), len(strings.Fields(normalize(s.Title)))); e != nil {
			return e
		}
		for _, g := range glossaryEntries(s.Title, s.Body) {
			result, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO glossary(document_id,section_id,term,term_norm,definition,definition_norm,kind) VALUES(?,?,?,?,?,?,?)", id, s.ID, g.Term, normalize(g.Term), g.Definition, normalize(g.Definition), g.Kind)
			if e != nil {
				return e
			}
			changed, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if changed == 0 {
				continue
			}
			gid, e := result.LastInsertId()
			if e != nil {
				return e
			}
			words := map[string]bool{}
			for _, word := range strings.Fields(normalize(g.Term + " " + g.Definition)) {
				words[word] = true
			}
			for word := range words {
				if _, e = tx.ExecContext(ctx, "INSERT INTO glossary_postings(term,glossary_id) VALUES(?,?)", word, gid); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

func rebuildEnrichment(ctx context.Context, tx *sql.Tx) error {
	rows, e := tx.QueryContext(ctx, "SELECT id FROM documents ORDER BY id")
	if e != nil {
		return e
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = enrichDocument(ctx, tx, id); e != nil {
			return e
		}
	}
	return nil
}

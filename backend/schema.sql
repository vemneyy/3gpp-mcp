PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS documents (
 id INTEGER PRIMARY KEY, spec TEXT NOT NULL UNIQUE, family TEXT NOT NULL CHECK(family IN ('3gpp','gsm')),
 series TEXT NOT NULL, title TEXT NOT NULL, version TEXT NOT NULL, url TEXT NOT NULL,
 sha256 TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS documents_series ON documents(family,series);
CREATE TABLE IF NOT EXISTS sections (
 id INTEGER PRIMARY KEY, document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
 number TEXT NOT NULL, title TEXT NOT NULL, page INTEGER NOT NULL CHECK(page>0),
 UNIQUE(document_id,number)
);
CREATE INDEX IF NOT EXISTS sections_document_order ON sections(document_id,id);
CREATE TABLE IF NOT EXISTS section_text (
 section_id INTEGER PRIMARY KEY REFERENCES sections(id) ON DELETE CASCADE,
 body TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS postings (
 term TEXT NOT NULL, section_id INTEGER NOT NULL REFERENCES sections(id) ON DELETE CASCADE,
 document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
 title_hits INTEGER NOT NULL, body_hits INTEGER NOT NULL,
 PRIMARY KEY(term,section_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS postings_section ON postings(section_id);
CREATE INDEX IF NOT EXISTS postings_document ON postings(document_id);
CREATE TABLE IF NOT EXISTS term_stats (
 term TEXT PRIMARY KEY, section_count INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TRIGGER IF NOT EXISTS postings_ai AFTER INSERT ON postings BEGIN
 INSERT INTO term_stats(term,section_count) VALUES(new.term,1)
 ON CONFLICT(term) DO UPDATE SET section_count=section_count+1;
END;
CREATE TRIGGER IF NOT EXISTS postings_ad AFTER DELETE ON postings BEGIN
 UPDATE term_stats SET section_count=section_count-1 WHERE term=old.term;
 DELETE FROM term_stats WHERE term=old.term AND section_count=0;
END;

CREATE TABLE IF NOT EXISTS document_search (
 document_id INTEGER PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
 title_norm TEXT NOT NULL, spec_compact TEXT NOT NULL, etsi_number TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS document_compact ON document_search(spec_compact);
CREATE INDEX IF NOT EXISTS document_etsi ON document_search(etsi_number);
CREATE TABLE IF NOT EXISTS document_postings (
 term TEXT NOT NULL, document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
 hits INTEGER NOT NULL, PRIMARY KEY(term,document_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS document_postings_document ON document_postings(document_id);
CREATE TABLE IF NOT EXISTS section_features (
 section_id INTEGER PRIMARY KEY REFERENCES sections(id) ON DELETE CASCADE,
 kind TEXT NOT NULL, body_length INTEGER NOT NULL, title_length INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS glossary (
 id INTEGER PRIMARY KEY,
 document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
 section_id INTEGER NOT NULL REFERENCES sections(id) ON DELETE CASCADE,
 term TEXT NOT NULL, term_norm TEXT NOT NULL, definition TEXT NOT NULL, definition_norm TEXT NOT NULL,
 kind TEXT NOT NULL, UNIQUE(section_id,term_norm,kind)
);
CREATE INDEX IF NOT EXISTS glossary_term ON glossary(term_norm);
CREATE INDEX IF NOT EXISTS glossary_document ON glossary(document_id);
PRAGMA user_version=3;
CREATE TABLE IF NOT EXISTS glossary_postings (
 term TEXT NOT NULL, glossary_id INTEGER NOT NULL REFERENCES glossary(id) ON DELETE CASCADE,
 PRIMARY KEY(term,glossary_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS glossary_postings_entry ON glossary_postings(glossary_id);

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
 number TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, page INTEGER NOT NULL CHECK(page>0),
 title_norm TEXT NOT NULL, body_norm TEXT NOT NULL, UNIQUE(document_id,number)
);
CREATE VIRTUAL TABLE IF NOT EXISTS search USING fts5(title_norm,body_norm,content='sections',content_rowid='id',tokenize='unicode61 remove_diacritics 0');
CREATE TRIGGER IF NOT EXISTS sections_ai AFTER INSERT ON sections BEGIN
 INSERT INTO search(rowid,title_norm,body_norm) VALUES(new.id,new.title_norm,new.body_norm); END;
CREATE TRIGGER IF NOT EXISTS sections_ad AFTER DELETE ON sections BEGIN
 INSERT INTO search(search,rowid,title_norm,body_norm) VALUES('delete',old.id,old.title_norm,old.body_norm); END;
CREATE TRIGGER IF NOT EXISTS sections_au AFTER UPDATE ON sections BEGIN
 INSERT INTO search(search,rowid,title_norm,body_norm) VALUES('delete',old.id,old.title_norm,old.body_norm);
 INSERT INTO search(rowid,title_norm,body_norm) VALUES(new.id,new.title_norm,new.body_norm); END;

CREATE TABLE IF NOT EXISTS term_counts (
 term TEXT NOT NULL, section_id INTEGER NOT NULL REFERENCES sections(id) ON DELETE CASCADE,
 title_hits INTEGER NOT NULL, body_hits INTEGER NOT NULL,
 PRIMARY KEY(term,section_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS term_counts_section ON term_counts(section_id);

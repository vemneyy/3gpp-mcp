package main

import (
	"context"
	"database/sql"
	"fmt"
)

// ensureSchema refuses implicit legacy upgrades: the operator stops the old
// writer and explicitly runs --migrate. The whole logical migration is atomic.
func ensureSchema(ctx context.Context, db *sql.DB, migrate bool) error {
	var version, exists int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > 3 {
		return fmt.Errorf("database schema %d is newer than this binary", version)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sections'").Scan(&exists); err != nil {
		return err
	}
	schema, _ := assets.ReadFile("schema.sql")
	if exists == 0 || version == 3 {
		if _, err := db.ExecContext(ctx, string(schema)); err != nil {
			return err
		}
		if migrate {
			_, err := db.ExecContext(ctx, "VACUUM; PRAGMA optimize")
			return err
		}
		return nil
	}
	if !migrate {
		return fmt.Errorf("legacy database: stop old indexer/backend, back up the database, then run --db <path> --migrate")
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=30000"); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if version < 2 {
		if _, err = tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS sections_ai;
 DROP TRIGGER IF EXISTS sections_ad; DROP TRIGGER IF EXISTS sections_au;
 DROP TABLE IF EXISTS search_vocab; DROP TABLE IF EXISTS search;
 ALTER TABLE sections RENAME TO legacy_sections;`); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(schema)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO sections(id,document_id,number,title,page)
 SELECT id,document_id,number,title,page FROM legacy_sections;
 INSERT INTO section_text(section_id,body) SELECT id,body FROM legacy_sections;
 INSERT INTO postings(term,section_id,document_id,title_hits,body_hits)
 SELECT t.term,t.section_id,s.document_id,t.title_hits,t.body_hits
 FROM term_counts t JOIN legacy_sections s ON s.id=t.section_id;
 DROP TABLE term_counts; DROP TABLE legacy_sections;`); err != nil {
			return err
		}
	} else {
		if _, err = tx.ExecContext(ctx, string(schema)); err != nil {
			return err
		}
	}
	if err = rebuildEnrichment(ctx, tx); err != nil {
		return err
	}

	var violations int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("foreign-key violations after migration: %d", violations)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Reclaim space previously occupied by FTS, duplicate normalized text and
	// the wide legacy sections table. Logical migration already committed.
	if _, err = db.ExecContext(ctx, "VACUUM; PRAGMA optimize"); err != nil {
		return fmt.Errorf("schema upgraded, space compaction failed (safe to rerun --migrate): %w", err)
	}
	return nil
}

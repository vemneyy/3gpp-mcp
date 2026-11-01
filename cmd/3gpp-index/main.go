// 3gpp-index builds a searchable SQLite database from ETSI PDFs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"

	"github.com/higebu/3gpp-mcp/internal/asn1index"
	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/higebu/3gpp-mcp/internal/etsi"
)

func main() {
	database := flag.String("db", "3gpp.db", "SQLite database path")
	base := flag.String("base", etsi.DefaultBase, "ETSI deliver directory URL")
	resume := flag.Bool("resume", false, "Resume an incomplete database and update changed versions")
	workers := flag.Int("workers", 4, "Number of parallel PDF downloads and conversions")
	flag.Parse()
	if *workers < 1 {
		log.Fatal("workers must be positive")
	}
	if _, err := os.Stat(*database); err == nil && !*resume {
		log.Fatal("database already exists; use --resume or choose a new path")
	} else if err != nil && !os.IsNotExist(err) {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	client := etsi.Client{Base: *base}
	docs, err := client.Discover(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Discovered %d specifications", len(docs))
	d, err := db.OpenReadWrite(*database)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		log.Fatal(err)
	}
	jobs := make(chan etsi.Document)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures, indexed, skipped int
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for doc := range jobs {
				if e := client.Import(ctx, d, doc); errors.Is(e, etsi.ErrNot3GPP) {
					log.Printf("Skipping non-3GPP ETSI publication %s", doc.URL)
					mu.Lock()
					skipped++
					mu.Unlock()
				} else if e != nil {
					log.Printf("%s v%s: %v", doc.ID, doc.Version, e)
					mu.Lock()
					failures++
					mu.Unlock()
				} else {
					log.Printf("Indexed %s v%s", doc.ID, doc.Version)
					mu.Lock()
					indexed++
					mu.Unlock()
				}
			}
		}()
	}
sendJobs:
	for _, doc := range docs {
		if *resume {
			versions, e := d.ListSpecVersions(ctx, doc.ID)
			if e != nil {
				log.Printf("%s: %v", doc.ID, e)
				mu.Lock()
				failures++
				mu.Unlock()
				continue
			}
			if len(versions) == 0 {
				// ETSI paths pad part numbers, while 3GPP cover pages may not.
				prefix, part, ok := strings.Cut(doc.ID, "-")
				if ok {
					if n, err := strconv.Atoi(part); err == nil {
						versions, e = d.ListSpecVersions(ctx, fmt.Sprintf("%s-%d", prefix, n))
						if e != nil {
							log.Printf("%s: %v", doc.ID, e)
							mu.Lock()
							failures++
							mu.Unlock()
							continue
						}
					}
				}
			}
			if len(versions) > 0 && versions[0].Version == doc.Version {
				ready, e := d.HasPDFMetadata(ctx, versions[0].ID, doc.Version)
				if e != nil {
					log.Printf("%s: %v", doc.ID, e)
					mu.Lock()
					failures++
					mu.Unlock()
					continue
				}
				if ready {
					continue
				}
			}
		}
		select {
		case jobs <- doc:
		case <-ctx.Done():
			break sendJobs
		}
	}
	close(jobs)
	wg.Wait()
	if _, err = asn1index.Rebuild(ctx, d); err != nil {
		log.Printf("ASN.1 index: %v", err)
		failures++
	}
	if failures > 0 || ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "Indexing incomplete: %d failed; database is partial\n", failures)
		os.Exit(1)
	}
	// A standalone database is safely mountable read-only after the WAL is checkpointed.
	if err := d.FinalizeForReadOnly(); err != nil {
		log.Fatalf("finalize database: %v", err)
	}
	log.Printf("Indexed %d specifications; skipped %d non-3GPP ETSI publications", indexed, skipped)
}

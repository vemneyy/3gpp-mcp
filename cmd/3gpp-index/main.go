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
	"time"

	"github.com/higebu/3gpp-mcp/internal/asn1index"
	"github.com/higebu/3gpp-mcp/internal/buildinfo"
	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/higebu/3gpp-mcp/internal/etsi"
	"github.com/higebu/3gpp-mcp/internal/rfc"
	"syscall"
)

var version = buildinfo.Version

func main() {
	showVersion := flag.Bool("version", false, "Print version and exit")
	scrapeWorkers := flag.Int("scrape-workers", 8, "Parallel ETSI version discovery workers")
	source := flag.String("source", "etsi", "Source to index: etsi, rfc or all")
	rfcBase := flag.String("rfc-base", rfc.DefaultBase, "RFC Editor base URL")
	database := flag.String("db", "3gpp.db", "SQLite database path")
	base := flag.String("base", etsi.DefaultBase, "ETSI deliver directory URL")
	resume := flag.Bool("resume", false, "Resume an incomplete database and update changed versions")
	workers := flag.Int("workers", 8, "Number of parallel PDF downloads and conversions")
	flag.Parse()
	if *showVersion {
		fmt.Println("3gpp-index " + version)
		return
	}
	if *scrapeWorkers < 1 {
		log.Fatal("scrape-workers must be positive")
	}
	if *workers < 1 {
		log.Fatal("workers must be positive")
	}
	if *source != "etsi" && *source != "rfc" && *source != "all" {
		log.Fatal("source must be etsi, rfc or all")
	}
	if *source == "rfc" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		runRFC(ctx, database, rfcBase, workers, resume)
		return
	}
	if _, err := os.Stat(*database); err == nil && !*resume {
		log.Fatal("database already exists; use --resume or choose a new path")
	} else if err != nil && !os.IsNotExist(err) {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := etsi.Client{Base: *base, Progress: func(message string) { log.Print(message) }}

	d, err := db.OpenReadWrite(*database)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		log.Fatal(err)
	}
	jobs := make(chan etsi.Document, *workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures, indexed, skipped, resumed, completed int
	started := time.Now()
	report := func() {
		log.Printf("Progress: completed=%d, indexed=%d, non-3GPP=%d, already indexed=%d, failed=%d; elapsed=%s (discovery streaming)", completed, indexed, skipped, resumed, failures, time.Since(started).Round(time.Second))
	}

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for doc := range jobs {
				if e := client.Import(ctx, d, doc); errors.Is(e, etsi.ErrNot3GPP) {
					log.Printf("Skipping non-3GPP ETSI publication %s", doc.URL)
					mu.Lock()
					skipped++
					completed++
					report()
					mu.Unlock()
				} else if e != nil {
					log.Printf("%s v%s: %v", doc.ID, doc.Version, e)
					mu.Lock()
					failures++
					completed++
					report()
					mu.Unlock()
				} else {
					log.Printf("Indexed %s v%s", doc.ID, doc.Version)
					mu.Lock()
					indexed++
					completed++
					report()
					mu.Unlock()
				}
			}
		}()
	}
	discoveryErr := client.Stream(ctx, *scrapeWorkers, func(doc etsi.Document) error {
		if *resume {
			versions, e := d.ListSpecVersions(ctx, doc.ID)
			if e != nil {
				log.Printf("%s: %v", doc.ID, e)
				mu.Lock()
				failures++
				mu.Unlock()
				return nil
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
							return nil
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
					return nil
				}
				if ready {
					mu.Lock()
					resumed++
					completed++
					report()
					mu.Unlock()
					return nil
				}
			}
		}
		select {
		case jobs <- doc:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(jobs)
	wg.Wait()
	if _, err = asn1index.Rebuild(ctx, d); err != nil {
		log.Printf("ASN.1 index: %v", err)
		if dropErr := d.DropASN1Index(); dropErr != nil {
			log.Printf("drop stale ASN.1 index: %v", dropErr)
		}
		failures++
	}
	if discoveryErr != nil {
		log.Printf("Discovery incomplete: %v", discoveryErr)
	}
	if failures > 0 || ctx.Err() != nil || discoveryErr != nil {
		fmt.Fprintf(os.Stderr, "Indexing incomplete: %d failed; database is partial\n", failures)
		os.Exit(1)
	}
	// A standalone database is safely mountable read-only after the WAL is checkpointed.
	if err := d.FinalizeForReadOnly(); err != nil {
		log.Fatalf("finalize database: %v", err)
	}
	log.Printf("Indexed %d specifications; skipped %d non-3GPP ETSI publications", indexed, skipped)
	if *source == "all" {
		if err := d.Close(); err != nil {
			log.Fatal(err)
		}
		runRFC(ctx, database, rfcBase, workers, resume)
	}
}

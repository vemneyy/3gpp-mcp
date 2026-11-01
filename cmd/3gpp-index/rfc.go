package main

import (
	"context"
	"fmt"
	"github.com/higebu/3gpp-mcp/internal/db"
	"github.com/higebu/3gpp-mcp/internal/rfc"
	"log"
	"strconv"
	"strings"
	"sync"
)

func runRFC(ctx context.Context, database, base *string, workers *int, resume *bool) {
	c := rfc.Client{Base: *base}
	entries, err := c.Discover(ctx)
	if err != nil {
		log.Fatal(err)
	}
	d, err := db.OpenReadWrite(*database)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		log.Fatal(err)
	}
	log.Printf("Discovered %d RFCs", len(entries))
	jobs := make(chan rfc.Entry)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done, failed, skipped := 0, 0, 0
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				n, _ := strconv.Atoi(strings.TrimPrefix(e.ID, "RFC"))
				id := fmt.Sprintf("RFC %d", n)
				if *resume {
					v, err := d.ListSpecVersions(ctx, id)
					if err != nil {
						mu.Lock()
						failed++
						done++
						log.Printf("%s: %v", id, err)
						mu.Unlock()
						continue
					}
					if len(v) > 0 {
						mu.Lock()
						skipped++
						done++
						mu.Unlock()
						continue
					}
				}
				err := c.Import(ctx, d, e)
				mu.Lock()
				done++
				if err != nil {
					failed++
					log.Printf("%s: %v", id, err)
				}
				if done%50 == 0 || err != nil {
					log.Printf("RFC progress %d/%d, failed=%d, skipped=%d", done, len(entries), failed, skipped)
				}
				mu.Unlock()
			}
		}()
	}
loop:
	for _, e := range entries {
		select {
		case jobs <- e:
		case <-ctx.Done():
			break loop
		}
	}
	close(jobs)
	wg.Wait()
	if failed > 0 || ctx.Err() != nil {
		log.Fatalf("RFC indexing incomplete: %d failed", failed)
	}
	if err = d.FinalizeForReadOnly(); err != nil {
		log.Fatal(err)
	}
	log.Printf("RFC indexing complete: %d documents (%d already indexed)", len(entries), skipped)
}

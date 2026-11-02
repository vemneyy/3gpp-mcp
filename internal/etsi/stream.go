package etsi

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type discoveryTask func(context.Context) ([]Document, error)

// Stream discovers complete per-spec version listings in a fixed worker pool
// and immediately emits the latest PDF to the download/conversion queue.
// emit runs serially; a blocked consumer applies bounded backpressure. Any
// failed listing aborts the run, which may already have imported some documents.
func (c Client) Stream(ctx context.Context, workers int, emit func(Document) error) error {
	if workers <= 0 {
		workers = 8
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan discoveryTask, workers)
	results := make(chan Document, workers)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	fail := func(err error) {
		if err != nil {
			once.Do(func() { firstErr = err; cancel() })
		}
	}
	wg.Go(func() {
		defer close(jobs)
		fail(c.discoveryTasks(ctx, func(task discoveryTask) error {
			select {
			case jobs <- task:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}))
	})
	for range workers {
		wg.Go(func() {
			for task := range jobs {
				if ctx.Err() != nil {
					return
				}
				docs, err := task(ctx)
				if err != nil {
					fail(err)
					return
				}
				for _, doc := range docs {
					select {
					case results <- doc:
					case <-ctx.Done():
						return
					}
				}
			}
		})
	}
	go func() { wg.Wait(); close(results) }()
	count := 0
	var emitErr error
	for doc := range results {
		if emitErr == nil && ctx.Err() == nil {
			count++
			emitErr = emit(doc)
			if emitErr != nil {
				cancel()
			}
		}
	}
	if emitErr != nil {
		return emitErr
	}
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no ETSI PDFs discovered")
	}
	return nil
}

func (c Client) discoveryTasks(ctx context.Context, yield func(discoveryTask) error) error {
	base := c.Base
	if base == "" {
		base = DefaultBase
	}
	for _, kind := range []string{"etsi_ts/", "etsi_tr/"} {
		root := join(base, kind)
		c.progress("Discovery: reading %s", root)
		groups, err := c.listing(ctx, root)
		if err != nil {
			return err
		}
		count := 0
		for _, group := range groups {
			if !groupRE.MatchString(group) {
				continue
			}
			count++
			c.progress("Discovery: %s%s", kind, group)
			specs, err := c.listing(ctx, join(root, group))
			if err != nil {
				return err
			}
			seen := make(map[string]bool)
			for _, spec := range specs {
				if !specRE.MatchString(spec) || seen[spec] {
					continue
				}
				seen[spec] = true
				if err := yield(func(ctx context.Context) ([]Document, error) {
					return c.latestPDF(ctx, join(join(root, group), spec), kind, spec)
				}); err != nil {
					return err
				}
			}
		}
		if count == 0 {
			return fmt.Errorf("no 3GPP directories found at %s", root)
		}
	}
	if err := yield(func(ctx context.Context) ([]Document, error) { return c.discoverMobileEN(ctx, join(base, "etsi_en/")) }); err != nil {
		return err
	}
	return yield(func(ctx context.Context) ([]Document, error) { return c.discoverGTS(ctx, join(base, "etsi_gts/")) })
}

func (c Client) latestPDF(ctx context.Context, address, kind, spec string) ([]Document, error) {
	c.progress("Discovery: checking %s", address)
	versions, err := c.listing(ctx, address)
	if err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	for _, v := range versions {
		vm := versionRE.FindStringSubmatch(v)
		if vm == nil {
			continue
		}
		vp := join(address, v)
		files, err := c.listing(ctx, vp)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			m := pdfRE.FindStringSubmatch(f)
			if m == nil || "1"+m[2]+"/" != spec || !strings.EqualFold(m[1], strings.TrimSuffix(strings.TrimPrefix(kind, "etsi_"), "/")) {
				continue
			}
			if m[3] != vm[1] || m[4] != vm[2] || m[5] != vm[3] {
				continue
			}
			a, _ := strconv.Atoi(m[3])
			b, _ := strconv.Atoi(m[4])
			d, _ := strconv.Atoi(m[5])
			number := m[2][:2] + "." + m[2][2:5]
			if len(m[2]) == 7 {
				number += "-" + m[2][5:]
			}
			return []Document{{ID: strings.ToUpper(m[1]) + " " + number, Version: fmt.Sprintf("%d.%d.%d", a, b, d), Series: m[2][:2], URL: join(vp, f)}}, nil
		}
	}
	return nil, nil
}

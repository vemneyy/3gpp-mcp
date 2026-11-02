package pipeline

import (
	"context"
	"net/http"
)

// Discover selects one latest matching version per spec and streams it to emit.
// A complete directory is filtered at once so archive ordering cannot cause an
// older version to overwrite a newer one. Local lists and cached listings use
// the same selector as live discovery.
func Discover(ctx context.Context, client *http.Client, list string, filter SpecFilter, cache bool, workers int, emit func(*SpecVersion) error) error {
	filter.LatestOnly = true
	batch := func(entries []string) error {
		var specs []*SpecVersion
		for _, entry := range entries {
			if spec := ParseSpecEntry(entry); spec != nil {
				specs = append(specs, spec)
			}
		}
		for _, spec := range FilterSpecs(specs, filter) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := emit(spec); err != nil {
				return err
			}
		}
		return nil
	}
	if list != "" {
		entries, err := LoadSpecList(list)
		if err != nil {
			return err
		}
		return batch(entries)
	}
	if filter.SpecID != "" {
		entries, err := FetchSpecZips(ctx, client, filter.SpecID, cache)
		if err != nil {
			return err
		}
		return batch(entries)
	}
	return StreamSpecList(ctx, client, filter.Series, cache, workers, batch)
}

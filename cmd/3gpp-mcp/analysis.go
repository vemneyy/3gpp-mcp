package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/higebu/3gpp-mcp/internal/dump"
	"github.com/higebu/3gpp-mcp/internal/tools"
)

func cmdSearchAll(args []string) {
	fs := flag.NewFlagSet("search-all", flag.ExitOnError)
	qf := addQueryFlags(fs, false)
	specs := fs.String("specs", "", "Optional comma-separated specification IDs")
	limit := fs.Int("limit", 0, "Results per page (default 10, max 200)")
	offset := fs.Int("offset", 0, "Global result offset")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: 3gpp-mcp search-all [options] <query>")
		exit(1)
		return
	}
	runQuery("search-all", func(ctx context.Context) error {
		src, cleanup, err := qf.openSource(false)
		if err != nil {
			return err
		}
		defer cleanup()
		var ids []string
		for _, id := range strings.Split(*specs, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
		result, err := src.DB.SearchAll(ctx, strings.Join(fs.Args(), " "), ids, *limit, *offset)
		if err != nil {
			return err
		}
		return printJSON(os.Stdout, result)
	})
}

func cmdAnalyzeDump(args []string) {
	fs := flag.NewFlagSet("analyze-dump", flag.ExitOnError)
	qf := addQueryFlags(fs, false)
	query := fs.String("query", "", "Optional query for related specifications")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: 3gpp-mcp analyze-dump [options] <capture.pcap|capture.pcapng|-> (input file is deleted)")
		exit(1)
		return
	}
	runQuery("analyze-dump", func(ctx context.Context) (err error) {
		var input io.Reader = os.Stdin
		if fs.Arg(0) != "-" {
			f, openErr := os.Open(fs.Arg(0))
			if openErr != nil {
				return openErr
			}
			info, statErr := f.Stat()
			if statErr != nil {
				_ = f.Close()
				return statErr
			}
			if !info.Mode().IsRegular() {
				_ = f.Close()
				return fmt.Errorf("capture must be a regular file")
			}
			input = f
			defer func() { err = errors.Join(err, f.Close(), os.Remove(fs.Arg(0))) }()
		}
		report, err := dump.Analyze(ctx, input)
		if err != nil {
			return err
		}
		src, cleanup, err := qf.openSource(false)
		if err != nil {
			return err
		}
		defer cleanup()
		result, err := tools.AnalyzeCapture(ctx, src.DB, report, *query)
		if err != nil {
			return err
		}
		return printJSON(os.Stdout, result)
	})
}

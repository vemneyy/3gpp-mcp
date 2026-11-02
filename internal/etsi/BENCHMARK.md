# ETSI PDF indexing benchmarks

Run the self-contained synthetic workload:

```sh
go test -run '^$' -bench 'Benchmark(ParseText|InsertSpec|ParseAndInsert)$' -benchmem ./internal/etsi
```

For a real document, obtain an ETSI PDF and run `pdftotext -layout document.pdf document.txt`, then:

```sh
ETSI_BENCH_TEXT=document.txt go test -run '^$' -bench 'Benchmark(ParseText|InsertSpec|ParseAndInsert)$' -benchtime=1x -count=3 -benchmem ./internal/etsi
```

The benchmarks time parsing, transactional SQLite insertion (including FTS and cross references), and the combined operation. SQLite uses a temporary file and its normal WAL settings. PDF download and `pdftotext` are not included. Insertion repeats the same specification when the benchmark runs more than once, so subsequent iterations also delete and replace existing indexed content.

## Example measurement

On an AMD EPYC 9V74, using 3,142,704 bytes of `pdftotext -layout` output from ETSI TS 23.501 v18.5.0, Go benchmark `-benchtime=1x -count=3`:

- Parsing: 35.5–40.8 ms, approximately 10.0 MB allocated per operation.
- SQLite insertion: 751–834 ms, 967 extracted sections, approximately 4.1 MB allocated per operation.
- Parse and insert: 798–861 ms, approximately 14.1 MB allocated per operation.

In a separate comparison using the same parsed sections and database setup, disabling the new reference-pattern prefilters resulted in 971–1,040 ms per insertion, versus 843–891 ms with them. The final measurement above was taken after a small change to page-header handling. The original parser measured 43.7–50.2 ms, but produced 1,069 sections before the table-of-contents validation; its output differs, so this is not a controlled speed comparison. One iteration per sample is sensitive to scheduling, caches and filesystem behavior. The combined operation was measured independently and its range need not equal the sum of the separate measurements. Re-run on the intended deployment hardware and corpus before extrapolating.

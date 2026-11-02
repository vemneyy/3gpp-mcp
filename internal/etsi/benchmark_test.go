package etsi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higebu/3gpp-mcp/internal/db"
)

// Set ETSI_BENCH_TEXT to pdftotext -layout output to benchmark a real document.
func benchmarkText(b *testing.B) string {
	b.Helper()
	if file := os.Getenv("ETSI_BENCH_TEXT"); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			b.Fatal(err)
		}
		return string(data)
	}
	var s strings.Builder
	s.WriteString("1 Scope\nScope of the specification.\n2 References\n[1] 3GPP TS 23.501: Architecture.\n")
	for i := 3; i < 503; i++ {
		fmt.Fprintf(&s, "%d Section %d\n", i, i)
		for j := 0; j < 12; j++ {
			s.WriteString("The system supports network functions and services as specified in 3GPP TS 23.502 [1].\n")
		}
	}
	return s.String()
}
func BenchmarkParseText(b *testing.B) {
	text := benchmarkText(b)
	doc := Document{ID: "TS 23.501", Version: "18.5.0", Series: "23"}
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := ParseText(doc, text); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkInsertSpec(b *testing.B) {
	text := benchmarkText(b)
	doc := Document{ID: "TS 23.501", Version: "18.5.0", Series: "23"}
	spec, sections, err := ParseText(doc, text)
	if err != nil {
		b.Fatal(err)
	}
	d, err := db.OpenReadWrite(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		b.Fatal(err)
	}
	b.Logf("input=%d bytes sections=%d", len(text), len(sections))
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err = d.InsertSpecWithSections(spec, sections); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkParseAndInsert(b *testing.B) {
	text := benchmarkText(b)
	doc := Document{ID: "TS 23.501", Version: "18.5.0", Series: "23"}
	d, err := db.OpenReadWrite(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	if err = d.InitSchema(); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		spec, sections, err := ParseText(doc, text)
		if err != nil {
			b.Fatal(err)
		}
		if err = d.InsertSpecWithSections(spec, sections); err != nil {
			b.Fatal(err)
		}
	}
}

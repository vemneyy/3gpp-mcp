package dump

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeTshark(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tshark"), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	return tmp
}

func assertNoCaptures(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("capture files remain: %v", entries)
	}
}

func TestAnalyzeLocalAndCleanup(t *testing.T) {
	tmp := fakeTshark(t, "printf 'NGAP\\t1\\t\\t1\\t\\t1\\t0.0\\tUE\\tgNB\\tInitialUEMessage\\nHTTP2\\t\\t1\\t0\\t1\\t2\\t0.1\\tAMF\\tSMF\\tHTTP2 request\\n'\n")
	for _, magic := range [][]byte{{0xd4, 0xc3, 0xb2, 0xa1}, {0x0a, 0x0d, 0x0d, 0x0a}} {
		report, err := Analyze(t.Context(), bytes.NewReader(magic))
		if err != nil {
			t.Fatal(err)
		}
		if report.Packets != 2 || report.Protocols["NGAP"] != 1 || len(report.Findings) != 4 || strings.Join(report.SpecificationQueries, ",") != "HTTP2,NGAP" {
			t.Fatalf("bad report: %+v", report)
		}
		assertNoCaptures(t, tmp)
	}
}

func TestAnalyzeFailureCleanup(t *testing.T) {
	tmp := fakeTshark(t, "exit 1\n")
	for _, input := range []string{"bad", "nope", "\xd4\xc3\xb2\xa1"} {
		if _, err := Analyze(t.Context(), strings.NewReader(input)); err == nil {
			t.Fatal("invalid capture succeeded")
		}
		assertNoCaptures(t, tmp)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Analyze(ctx, strings.NewReader("\xd4\xc3\xb2\xa1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	assertNoCaptures(t, tmp)
}

func TestAnalyzeReaderFailureCleanup(t *testing.T) {
	tmp := fakeTshark(t, "exit 0\n")
	if _, err := Analyze(t.Context(), &failedReader{}); err == nil {
		t.Fatal("reader error ignored")
	}
	assertNoCaptures(t, tmp)
}

type failedReader struct{}

func (*failedReader) Read([]byte) (int, error) { return 0, errors.New("invalid upload") }

// Uses a real dissector when installed. Both capture formats contain the same
// synthetic Ethernet/IPv4/UDP packet; no capture fixtures or user traffic persist.
func TestAnalyzeRealTshark(t *testing.T) {
	if _, err := exec.LookPath("tshark"); err != nil {
		t.Skip("tshark not installed")
	}
	packet := []byte{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 8, 0,
		0x45, 0, 0, 28, 0, 0, 0, 0, 64, 17, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8,
		0x30, 0x39, 0xd4, 0x31, 0, 8, 0, 0,
	}
	var pcap bytes.Buffer
	put := func(b *bytes.Buffer, values ...uint32) {
		for _, v := range values {
			if err := binary.Write(b, binary.LittleEndian, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	put(&pcap, 0xa1b2c3d4, 0x00040002, 0, 0, 65535, 1, 0, 0, uint32(len(packet)), uint32(len(packet)))
	pcap.Write(packet)
	var pcapng bytes.Buffer
	put(&pcapng, 0x0a0d0d0a, 28, 0x1a2b3c4d, 1, 0xffffffff, 0xffffffff, 28)
	put(&pcapng, 1, 20, 1, 65535, 20)
	put(&pcapng, 6, 76, 0, 0, 1, uint32(len(packet)), uint32(len(packet)))
	pcapng.Write(packet)
	pcapng.Write([]byte{0, 0})
	put(&pcapng, 76)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	for format, input := range map[string][]byte{"pcap": pcap.Bytes(), "pcapng": pcapng.Bytes()} {
		t.Run(format, func(t *testing.T) {
			report, err := Analyze(t.Context(), bytes.NewReader(input))
			if err != nil {
				t.Fatal(err)
			}
			if report.Format != format || report.Packets != 1 || report.Protocols["UDP"] != 1 || len(report.Samples) != 1 || report.Samples[0].Source != "1.2.3.4" {
				t.Fatalf("unexpected real dissection: %+v", report)
			}
			assertNoCaptures(t, tmp)
		})
	}
}

func TestAnalyzeCancelsBlockedUpload(t *testing.T) {
	tmp := fakeTshark(t, "exit 0\n")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input := &blockingUpload{started: make(chan struct{}), closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := Analyze(ctx, input); done <- err }()
	<-input.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("upload cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation left upload blocked")
	}
	assertNoCaptures(t, tmp)
}

type blockingUpload struct{ started, closed chan struct{} }

func (r *blockingUpload) Read([]byte) (int, error) {
	close(r.started)
	<-r.closed
	return 0, errors.New("upload closed")
}
func (r *blockingUpload) Close() error { close(r.closed); return nil }

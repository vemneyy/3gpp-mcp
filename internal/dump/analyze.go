// Package dump performs offline PCAP/PCAPNG analysis. Captures are never cached.
package dump

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxBytes = 64 << 20
const MaxPackets = 10000
const MaxSamples = 50

// Bound concurrent dissectors independently of the number of HTTP/MCP callers.
var analysisSlots = make(chan struct{}, 2)

type Finding struct {
	Kind    string `json:"kind"`
	Packets int    `json:"packets"`
}

// Packet is a bounded decoded summary, not a copy of packet bytes.
type Packet struct {
	Number      string `json:"number"`
	Time        string `json:"time_relative"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Protocol    string `json:"protocol"`
	Info        string `json:"info"`
}

type Report struct {
	Samples              []Packet       `json:"packet_samples"`
	Format               string         `json:"format"`
	Bytes                int64          `json:"bytes"`
	Packets              int            `json:"packets_analyzed"`
	PacketLimit          int            `json:"packet_limit"`
	MayBeTruncated       bool           `json:"may_be_truncated"`
	Protocols            map[string]int `json:"protocols"`
	Findings             []Finding      `json:"findings"`
	SpecificationQueries []string       `json:"specification_queries"`
	Note                 string         `json:"note"`
}

// Analyze consumes uploaded capture bytes, dissects them with a local tshark,
// and removes all temporary data before returning, also on failure/cancellation.
// It does not execute shell commands or resolve network addresses.
func Analyze(ctx context.Context, input io.Reader) (report *Report, err error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	select {
	case analysisSlots <- struct{}{}:
		defer func() { <-analysisSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	executable, err := exec.LookPath("tshark")
	if err != nil {
		return nil, fmt.Errorf("local PCAP analysis requires tshark (Wireshark CLI): %w", err)
	}
	dir, err := os.MkdirTemp("", "3gpp-dump-")
	if err != nil {
		return nil, fmt.Errorf("create capture workspace: %w", err)
	}
	defer func() {
		if cleanup := os.RemoveAll(dir); cleanup != nil {
			report = nil
			err = errors.Join(err, fmt.Errorf("delete capture workspace: %w", cleanup))
		}
	}()
	path := filepath.Join(dir, "capture")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("create capture: %w", err)
	}
	stopClose := func() bool { return true }
	if closer, ok := input.(io.ReadCloser); ok {
		stopClose = context.AfterFunc(ctx, func() { _ = closer.Close() })
	}
	n, copyErr := io.Copy(f, io.LimitReader(input, MaxBytes+1))
	stopClose()
	closeErr := f.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if copyErr != nil {
		return nil, fmt.Errorf("read capture: %w", copyErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close capture: %w", closeErr)
	}
	if n > MaxBytes {
		return nil, fmt.Errorf("capture exceeds %d bytes", MaxBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	header := make([]byte, 4)
	capture, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	_, readErr := io.ReadFull(capture, header)
	_ = capture.Close()
	if readErr != nil {
		return nil, fmt.Errorf("capture header is truncated")
	}
	format := "pcap"
	switch binary.BigEndian.Uint32(header) {
	case 0xa1b2c3d4, 0xd4c3b2a1, 0xa1b23c4d, 0x4d3cb2a1:
	case 0x0a0d0d0a:
		format = "pcapng"
	default:
		return nil, fmt.Errorf("unsupported capture: expected PCAP or PCAPNG")
	}
	// No packet payload is emitted. -n prevents DNS lookups; -r is
	// offline input. The timeout, file cap and packet cap bound dissection work.
	cmd := exec.CommandContext(ctx, executable, "-n", "-r", path, "-c", "10000", "-T", "fields",
		"-E", "occurrence=f", "-e", "_ws.col.Protocol", "-e", "tcp.analysis.retransmission",
		"-e", "tcp.analysis.lost_segment", "-e", "tcp.flags.reset", "-e", "_ws.malformed",
		"-e", "frame.number", "-e", "frame.time_relative", "-e", "_ws.col.Source", "-e", "_ws.col.Destination", "-e", "_ws.col.Info")
	// Isolate Wireshark preferences from uploaded captures.
	cmd.Env = append(os.Environ(), "WIRESHARK_CONFIG_DIR="+dir, "XDG_CONFIG_HOME="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("capture output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start tshark: %w", err)
	}
	report = &Report{Format: format, Bytes: n, PacketLimit: MaxPackets, Protocols: map[string]int{}, Findings: []Finding{}, SpecificationQueries: []string{},
		Note: "Offline packet observations, not a standards-compliance verdict. Encrypted traffic requires decryption before protocol details can be inspected. No capture bytes are retained by the server."}
	counts := make(map[string]int)
	scanner := bufio.NewScanner(io.LimitReader(stdout, 32<<20))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), "\t", 10)
		if len(fields) != 10 {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("unexpected tshark output")
		}
		report.Packets++
		protocol := fields[0]
		if protocol == "" {
			protocol = "unknown"
		}
		// Protocol labels come from dissectors; bound the response cardinality.
		if len(report.Protocols) >= 128 {
			if _, ok := report.Protocols[protocol]; !ok {
				protocol = "other"
			}
		}
		report.Protocols[protocol]++
		anomaly := fields[1] != "" || fields[2] != "" || fields[3] == "1" || fields[3] == "True" || fields[4] != ""
		if len(report.Samples) < 20 || (anomaly && len(report.Samples) < MaxSamples) {
			report.Samples = append(report.Samples, Packet{Number: fields[5], Time: fields[6], Source: fields[7], Destination: fields[8], Protocol: protocol, Info: truncate(fields[9], 512)})
		}
		if fields[1] != "" {
			counts["tcp_retransmission"]++
		}
		if fields[2] != "" {
			counts["tcp_previous_segment_not_captured"]++
		}
		if fields[3] == "1" || fields[3] == "True" {
			counts["tcp_reset"]++
		}
		if fields[4] != "" {
			counts["malformed_packet"]++
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("read dissection: %w", scanErr)
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("tshark could not dissect capture: %w", err)
	}
	report.MayBeTruncated = report.Packets >= MaxPackets
	for kind, n := range counts {
		report.Findings = append(report.Findings, Finding{kind, n})
	}
	sort.Slice(report.Findings, func(i, j int) bool { return report.Findings[i].Kind < report.Findings[j].Kind })
	queries := map[string]bool{}
	for protocol := range report.Protocols {
		upper := strings.ToUpper(protocol)
		for _, term := range []string{"NGAP", "S1AP", "XNAP", "F1AP", "RRC", "NAS", "PFCP", "GTP", "SIP", "DIAMETER", "SCTP", "HTTP2"} {
			if strings.Contains(upper, term) {
				queries[term] = true
			}
		}
	}
	for q := range queries {
		report.SpecificationQueries = append(report.SpecificationQueries, q)
	}
	sort.Strings(report.SpecificationQueries)
	return report, nil
}

// Keep decoded summaries bounded without splitting a UTF-8 code point.
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

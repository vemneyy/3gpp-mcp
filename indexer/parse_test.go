package main

import (
	"os"
	"strings"
	"testing"
)

func TestNormalization(t *testing.T) {
	for _, s := range []string{"PDU-session / establishment", "ＰＤＵ session establishment", "PDU_session—establishment"} {
		if normalize(s) != "pdu session establishment" {
			t.Fatal(normalize(s))
		}
	}
	if normalize("Café e\u0301") != "cafe e" {
		t.Fatal("accents")
	}
}
func TestPrintedPagesAndSectionBoundaries(t *testing.T) {
	text := "ETSI TS V1.0.0\nTest specification\n(3GPP TS 23.501)\fContents\n1  Scope ........... 2\n2  PDU-session .......... 2\n\f1   Scope\nfirst body\n2   PDU-session\nsecond body\f"
	d, e := parsePDF(text)
	if e != nil {
		t.Fatal(e)
	}
	if len(d.Sections) != 2 || d.Sections[1].Page != 2 || strings.Contains(d.Sections[0].Body, "second") {
		t.Fatalf("%+v", d)
	}
}
func TestRecoversStaleTOCPage(t *testing.T) {
	_, e := parsePDF("3GPP TS 23.501\fContents\n1  Scope ..... 1\n\f1  Scope\ntext")
	if e != nil {
		t.Fatal(e)
	}
}
func TestRealPDF(t *testing.T) {
	p := os.Getenv("ETSI_TEST_TEXT")
	if p == "" {
		t.Skip("set ETSI_TEST_TEXT for live PDF integration")
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	d, e := parsePDF(string(b))
	if e != nil {
		t.Fatal(e)
	}
	if len(d.Sections) < 50 {
		t.Fatal(len(d.Sections))
	}
	t.Logf("%s: %d sections, title %s", d.Spec, len(d.Sections), d.Title)
}

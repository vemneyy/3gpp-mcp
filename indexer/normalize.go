package main

import (
	"golang.org/x/text/unicode/norm"
	"strings"
	"unicode"
)

func normalize(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		switch {
		case unicode.IsMark(r):
			continue
		case unicode.IsLetter(r) || unicode.Is(unicode.Nd, r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

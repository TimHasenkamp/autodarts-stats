// Package names normalisiert Spielernamen, damit derselbe Gastname ueber
// viele Matches hinweg derselben Identitaet zugeordnet wird.
package names

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var replacer = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"Ä", "ae", "Ö", "oe", "Ü", "ue", "ẞ", "ss",
)

// Normalize: trim, lowercase, Mehrfach-Whitespace zusammenziehen,
// Umlaute/ß nach ae/oe/ue/ss, sonstige Diakritika entfernen.
// Kein Fuzzy-Matching.
func Normalize(name string) string {
	s := replacer.Replace(strings.TrimSpace(name))
	s = norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue // kombinierende Akzente
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

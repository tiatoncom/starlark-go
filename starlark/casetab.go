package starlark

//go:generate go run mkcasetables.go

import (
	"unicode"
)

// The tables of the case maps (upper, lower, title, capitalize: see
// upperLowerLen and titleCaseLen in library.go). They are static: generated
// by mkcasetables.go from the package unicode of the Go that generates them
// (casetables.go, with the version of Unicode it was made from), so that the
// first call of a case map costs what the second does. TestCaseTables_...
// (casetab_test.go) builds them again from package unicode and fails if the
// Go that runs the tests has another Unicode: regenerate with go generate.

// caseWidthLimit bounds the runes that have a case mapping (the last is
// U+1E943): beyond it the mapping is called.
const caseWidthLimit = 0x20000

// caseWidths holds, for each direction, the bit of every rune below
// caseWidthLimit whose upper (or lower, or title) case is not as wide as it
// is in UTF-8 (U+023F, 2 bytes, to U+2C7E, 3): the case map of the rest
// keeps the size of the rune, and is not called to find it out.
type caseWidths struct{ upper, lower, title [caseWidthLimit / 64]uint64 }

// caseWidthTables returns the table of the widths.
func caseWidthTables() *caseWidths { return &generatedCaseWidths }

// isCasedAfter is isCasedRune of the lower case (or, if toTitle, the title
// case) of the non-ASCII rune r, by the table generatedCasedAfter: [0] for the
// lower case and [1] for the title case, a bit for every rune from U+0080 to
// caseWidthLimit (a rune is cased also by an orbit of SimpleFold that has no
// mapping, as U+00DF has, and U+0130 is not cased but its lower case is).
func isCasedAfter(r rune, toTitle bool) bool {
	if r < caseWidthLimit {
		k := 0
		if toTitle {
			k = 1
		}
		return generatedCasedAfter[k][r>>6]&(1<<(r&63)) != 0
	}
	if toTitle {
		return isCasedRune(unicode.ToTitle(r))
	}
	return isCasedRune(unicode.ToLower(r))
}

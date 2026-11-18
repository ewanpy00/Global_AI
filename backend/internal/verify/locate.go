package verify

import (
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// locate finds a claim's quoted span in the answer and returns byte offsets.
// Models quote "verbatim" but often drop markdown, change quote styles or whitespace,
// so matching falls back from exact → normalized. `from` prefers matches after the
// previous claim so repeated phrases highlight in reading order.
func locate(text, quote string, from int) (start, end int, ok bool) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return 0, 0, false
	}
	if from < 0 || from > len(text) {
		from = 0
	}
	if i := strings.Index(text[from:], quote); i >= 0 {
		return from + i, from + i + len(quote), true
	}
	if i := strings.Index(text, quote); i >= 0 {
		return i, i + len(quote), true
	}

	normText, idx := normalize(text)
	normQuote, _ := normalize(quote)
	normQuote = strings.TrimSpace(normQuote)
	if normQuote == "" {
		return 0, 0, false
	}
	pos := -1
	// Translate `from` into normalized coordinates.
	nfrom := 0
	for nfrom < len(idx) && idx[nfrom] < from {
		nfrom++
	}
	if i := strings.Index(normText[nfrom:], normQuote); i >= 0 {
		pos = nfrom + i
	} else if i := strings.Index(normText, normQuote); i >= 0 {
		pos = i
	}
	if pos < 0 {
		return 0, 0, false
	}
	last := pos + len(normQuote) - 1
	start = idx[pos]
	_, size := utf8.DecodeRuneInString(text[idx[last]:])
	end = idx[last] + size
	return start, end, true
}

// normalize lowercases, collapses whitespace, unifies quotes/dashes and drops markdown
// emphasis characters. idx[i] is the byte offset in the original text of normalized byte i.
func normalize(s string) (string, []int) {
	var b strings.Builder
	idx := make([]int, 0, len(s))
	lastSpace := false
	for i, r := range s {
		switch r {
		case '*', '_', '`':
			continue
		case '“', '”', '„', '«', '»':
			r = '"'
		case '‘', '’':
			r = '\''
		case '–', '—', '−':
			r = '-'
		case ' ':
			r = ' '
		}
		if unicode.IsSpace(r) {
			if lastSpace {
				continue
			}
			lastSpace = true
			r = ' '
		} else {
			lastSpace = false
		}
		r = unicode.ToLower(r)
		n := utf8.RuneLen(r)
		b.WriteRune(r)
		for k := 0; k < n; k++ {
			idx = append(idx, i)
		}
	}
	return b.String(), idx
}

// utf16Offset converts a byte offset in s to a UTF-16 code unit offset (JavaScript indexing).
func utf16Offset(s string, byteOff int) int {
	n := 0
	for _, r := range s[:byteOff] {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

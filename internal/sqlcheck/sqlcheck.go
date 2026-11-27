package sqlcheck

import (
	"strings"
	"unicode"
)

const (
	LoTag = "{{lo}}"
	HiTag = "{{hi}}"
)

// Statements mecah SQL per ';' di level teratas. String, identifier, komentar,
// dan dollar-quote ($$ ... $$) nggak ikut kepecah.
func Statements(sql string) []string {
	var out []string
	start := 0
	flush := func(end int) {
		if st := strings.TrimSpace(stripComments(sql[start:end])); st != "" {
			out = append(out, st)
		}
	}
	for i := 0; i < len(sql); {
		if sql[i] == ';' {
			flush(i)
			i++
			start = i
			continue
		}
		i = skip(sql, i)
	}
	flush(len(sql))
	return out
}

// TxControl ngecek ada statement kontrol transaksi (COMMIT, BEGIN, dll.) atau nggak.
// Balikin keyword yang ketemu.
func TxControl(sql string) (string, bool) {
	for _, st := range Statements(sql) {
		words := strings.Fields(strings.ToUpper(st))
		if len(words) == 0 {
			continue
		}
		w := strings.TrimRight(words[0], "(")
		switch w {
		case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "ABORT", "SAVEPOINT", "RELEASE":
			return w, true
		case "PREPARE":
			if len(words) > 1 && words[1] == "TRANSACTION" {
				return "PREPARE TRANSACTION", true
			}
		}
	}
	return "", false
}

func HasBatchTags(sql string) bool {
	return strings.Contains(sql, LoTag) && strings.Contains(sql, HiTag)
}

// BindBatch ganti {{lo}}/{{hi}} jadi $1/$2 biar nilainya di-bind, bukan disambung string.
func BindBatch(sql string) string {
	return strings.NewReplacer(LoTag, "$1", HiTag, "$2").Replace(sql)
}

// skip maju satu token mulai dari i (string, identifier, komentar, dollar-quote, atau 1 byte).
func skip(s string, i int) int {
	switch {
	case s[i] == '\'':
		esc := i > 0 && (s[i-1] == 'E' || s[i-1] == 'e') && (i < 2 || !isIdent(s[i-2]))
		return quoted(s, i+1, '\'', esc)
	case s[i] == '"':
		return quoted(s, i+1, '"', false)
	case strings.HasPrefix(s[i:], "--"):
		if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
			return i + j + 1
		}
		return len(s)
	case strings.HasPrefix(s[i:], "/*"):
		return blockComment(s, i)
	case s[i] == '$' && (i == 0 || !isIdent(s[i-1])):
		if tag, ok := dollarTag(s, i); ok {
			if j := strings.Index(s[i+len(tag):], tag); j >= 0 {
				return i + len(tag) + j + len(tag)
			}
			return len(s)
		}
	}
	return i + 1
}

func quoted(s string, i int, q byte, backslash bool) int {
	for i < len(s) {
		switch {
		case backslash && s[i] == '\\':
			i += 2
		case s[i] == q && i+1 < len(s) && s[i+1] == q:
			i += 2
		case s[i] == q:
			return i + 1
		default:
			i++
		}
	}
	return len(s)
}

func blockComment(s string, i int) int {
	depth := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(s[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(s)
}

func dollarTag(s string, i int) (string, bool) {
	j := i + 1
	for j < len(s) && s[j] != '$' {
		c := rune(s[j])
		if !(c == '_' || unicode.IsLetter(c) || (j > i+1 && unicode.IsDigit(c))) {
			return "", false
		}
		j++
	}
	if j >= len(s) {
		return "", false
	}
	return s[i : j+1], true
}

// isIdent: karakter lanjutan identifier versi Postgres (scan.l), termasuk $ dan byte non-ASCII.
// Jadi a$$x$ itu satu identifier, bukan awal dollar-quote.
func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func stripComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "--") || strings.HasPrefix(s[i:], "/*") {
			i = skip(s, i)
			b.WriteByte(' ')
			continue
		}
		j := skip(s, i)
		b.WriteString(s[i:j])
		i = j
	}
	return b.String()
}

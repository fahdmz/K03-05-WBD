package obs

import "regexp"

var (
	quotedPassword = regexp.MustCompile(`(?i)(password\s*=?\s*)'(?:[^']|'')*'`)
	plainPassword  = regexp.MustCompile(`(?i)(password\s*=\s*)[^\s']+`)
	urlPassword    = regexp.MustCompile(`(\w+://[^:/@\s]+:)[^@\s]+@`)
)

func Redact(s string) string {
	s = quotedPassword.ReplaceAllString(s, "${1}'[REDACTED]'")
	s = plainPassword.ReplaceAllString(s, "${1}[REDACTED]")
	s = urlPassword.ReplaceAllString(s, "${1}[REDACTED]@")
	return s
}

func safeParams(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case nil, bool, int, int32, int64, float64:
			out[i] = v
		default:
			out[i] = "[REDACTED]"
		}
	}
	return out
}

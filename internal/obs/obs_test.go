package obs

import "testing"

func TestRedact(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"sql password", "CREATE ROLE app PASSWORD 'rahasia'", "CREATE ROLE app PASSWORD '[REDACTED]'"},
		{"dsn password", "host=db password=abc123 user=x", "host=db password=[REDACTED] user=x"},
		{"url password", "postgres://target:target@localhost:5433/shop", "postgres://target:[REDACTED]@localhost:5433/shop"},
		{"kolom biasa", "SELECT password_hash FROM users WHERE id = $1", "SELECT password_hash FROM users WHERE id = $1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Redact(c.in); got != c.want {
				t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSafeParams(t *testing.T) {
	got := safeParams([]any{int64(7), "token-rahasia", true, nil})
	want := []any{int64(7), "[REDACTED]", true, nil}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("param %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSQLEvent(t *testing.T) {
	cases := map[string]string{
		"commit":                             "sql.commit",
		" ROLLBACK ":                         "sql.rollback",
		"begin isolation level serializable": "sql.begin",
		"SELECT 1":                           "sql.result",
	}
	for in, want := range cases {
		if got := sqlEvent(in); got != want {
			t.Errorf("sqlEvent(%q) = %q, want %q", in, got, want)
		}
	}
}

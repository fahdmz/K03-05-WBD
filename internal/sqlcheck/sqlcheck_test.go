package sqlcheck

import "testing"

func TestStatements(t *testing.T) {
	cases := []struct {
		sql  string
		want int
	}{
		{"SELECT 1", 1},
		{"SELECT 1; SELECT 2;", 2},
		{"  ;; ", 0},
		{"SELECT ';'; SELECT 2", 2},
		{`SELECT "a;b" FROM t`, 1},
		{"SELECT 1 -- ; komentar\n; SELECT 2", 2},
		{"SELECT /* ; /* nested ; */ */ 1", 1},
		{"DO $$ BEGIN PERFORM 1; END $$; SELECT 1", 2},
		{"DO $fn$ BEGIN RAISE NOTICE 'x;y'; END $fn$", 1},
		{`SELECT E'it\'s ; fine'`, 1},
		{"SELECT 'it''s ; fine'", 1},
	}
	for _, c := range cases {
		if got := len(Statements(c.sql)); got != c.want {
			t.Errorf("Statements(%q) = %d, mau %d", c.sql, got, c.want)
		}
	}
}

func TestTxControl(t *testing.T) {
	bad := []string{
		"CREATE TABLE t (id INT); COMMIT; SELECT 1/0",
		"begin; update t set a = 1",
		"UPDATE t SET a = 1;\nrollback",
		"SAVEPOINT s1",
		"release savepoint s1",
		"END",
		"ABORT",
		"START TRANSACTION",
		"PREPARE TRANSACTION 'x'",
		"-- awal\n  COMMIT",
	}
	for _, s := range bad {
		if _, ok := TxControl(s); !ok {
			t.Errorf("TxControl(%q) harusnya ketahuan", s)
		}
	}
	good := []string{
		"CREATE TABLE commits (id INT)",
		"UPDATE t SET note = 'COMMIT; ROLLBACK'",
		"DO $$ BEGIN UPDATE t SET a = 1; END $$",
		"PREPARE q AS SELECT 1",
		"SELECT 1 -- commit\n",
		`UPDATE "commit" SET x = 1`,
	}
	for _, s := range good {
		if kw, ok := TxControl(s); ok {
			t.Errorf("TxControl(%q) salah nangkep %s", s, kw)
		}
	}
}

func TestBatchTags(t *testing.T) {
	sql := "UPDATE t SET p = p * 1.1 WHERE id > {{lo}} AND id <= {{hi}}"
	if !HasBatchTags(sql) {
		t.Fatal("tag batch harusnya ada")
	}
	if HasBatchTags("UPDATE t SET p = 1 WHERE id > {{lo}}") {
		t.Fatal("cuma lo, harusnya false")
	}
	want := "UPDATE t SET p = p * 1.1 WHERE id > $1 AND id <= $2"
	if got := BindBatch(sql); got != want {
		t.Fatalf("BindBatch = %q", got)
	}
}

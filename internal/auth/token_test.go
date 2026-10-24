package auth

import "testing"

func TestNewTokenAcakDanUnik(t *testing.T) {
	seen := map[string]bool{}

	for i := 0; i < 1000; i++ {
		tok, err := newToken()
		if err != nil {
			t.Fatal(err)
		}

		if len(tok) != 43 {
			t.Fatalf("panjang token %d, harusnya 43 karakter", len(tok))
		}

		if seen[tok] {
			t.Fatal("ada token kembar")
		}

		seen[tok] = true
	}
}

func TestHashToken(t *testing.T) {
	h := hashToken("abc")

	if h != hashToken("abc") {
		t.Error("hash dari token yang sama harus selalu sama")
	}

	if h == hashToken("abd") {
		t.Error("token berbeda harus menghasilkan hash berbeda")
	}

	if h == "abc" || len(h) != 64 {
		t.Error("hash harus SHA-256 dalam hex, 64 karakter")
	}
}

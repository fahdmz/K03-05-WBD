package auth

import "testing"

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("rahasia123")
	if err != nil {
		t.Fatal(err)
	}

	if hash == "rahasia123" {
		t.Fatal("hash salah")
	}

	if !CheckPassword(hash, "rahasia123") {
		t.Error("password benar harus cocok")
	}

	if CheckPassword(hash, "salah") {
		t.Error("password salah tidak boleh cocok")
	}

	other, err := HashPassword("rahasia123")

	if err != nil {
		t.Fatal(err)
	}
	if other == hash {
		t.Error("dua hash dari password yang sama harus berbeda")
	}
}

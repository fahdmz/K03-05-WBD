package main

import (
	"strings"
	"testing"
)

func TestSeedAdminValidation(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		username string
		password string
		want     string
	}{
		{
			name:     "missing database URL",
			username: "admin",
			password: "password123",
			want:     "DATABASE_URL",
		},
		{
			name:     "empty username",
			url:      "postgres://unused",
			username: "   ",
			password: "password123",
			want:     "ADMIN_USERNAME",
		},
		{
			name:     "username too long",
			url:      "postgres://unused",
			username: strings.Repeat("a", 101),
			password: "password123",
			want:     "ADMIN_USERNAME",
		},
		{
			name:     "missing password",
			url:      "postgres://unused",
			username: "admin",
			want:     "ADMIN_PASSWORD",
		},
		{
			name:     "password too short",
			url:      "postgres://unused",
			username: "admin",
			password: "short",
			want:     "ADMIN_PASSWORD",
		},
		{
			name:     "password exceeds bcrypt byte limit",
			url:      "postgres://unused",
			username: "admin",
			password: strings.Repeat("a", 73),
			want:     "ADMIN_PASSWORD",
		},
		{
			name:     "multibyte password exceeds byte limit",
			url:      "postgres://unused",
			username: "admin",
			password: strings.Repeat("界", 25),
			want:     "ADMIN_PASSWORD",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("DATABASE_URL", tt.url)
			t.Setenv("ADMIN_USERNAME", tt.username)
			t.Setenv("ADMIN_PASSWORD", tt.password)

			err := run()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want reference to %s", err, tt.want)
			}
			if tt.password != "" && strings.Contains(err.Error(), tt.password) {
				t.Fatal("error exposes password")
			}
		})
	}
}

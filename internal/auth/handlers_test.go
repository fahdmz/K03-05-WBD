package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	s, _ := newTestService(t)
	createUser(t, s, "budi", "rahasia123", true)
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	return mux
}

func doLogin(t *testing.T, mux *http.ServeMux, password string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"username":"budi","password":"` + password + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session_id" {
			return c
		}
	}
	t.Fatal("cookie session_id tidak ada di response")
	return nil
}

func TestLoginHandlerCookieAman(t *testing.T) {
	mux := newTestMux(t)
	rec := doLogin(t, mux, "rahasia123")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}

	cookie := sessionCookie(t, rec)
	if !cookie.HttpOnly {
		t.Error("cookie harus HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Error("cookie harus SameSite=Lax")
	}
	if strings.Contains(rec.Body.String(), cookie.Value) {
		t.Error("session ID tidak boleh muncul di body response")
	}

	var resp sessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.CSRFToken == "" || resp.User.Username != "budi" {
		t.Errorf("isi response tidak sesuai: %+v", resp)
	}
}

func TestLoginHandlerPasswordSalah(t *testing.T) {
	mux := newTestMux(t)
	rec := doLogin(t, mux, "salah")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, harusnya 401", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("login gagal tidak boleh mengirim cookie")
	}
}

func TestMeButuhLogin(t *testing.T) {
	mux := newTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tanpa cookie harusnya 401, dapat %d", rec.Code)
	}

	cookie := sessionCookie(t, doLogin(t, mux, "rahasia123"))
	req = httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dengan cookie harusnya 200, dapat %d", rec.Code)
	}
}

func TestCSRFdanLogout(t *testing.T) {
	mux := newTestMux(t)
	login := doLogin(t, mux, "rahasia123")
	cookie := sessionCookie(t, login)
	var resp sessionResponse
	if err := json.Unmarshal(login.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"tanpa token CSRF", "", http.StatusForbidden},
		{"token CSRF salah", "salah", http.StatusForbidden},
		{"token CSRF benar", resp.CSRFToken, http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
			req.AddCookie(cookie)
			if c.header != "" {
				req.Header.Set("X-CSRF-Token", c.header)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("status %d, harusnya %d", rec.Code, c.want)
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("setelah logout harusnya 401, dapat %d", rec.Code)
	}
}

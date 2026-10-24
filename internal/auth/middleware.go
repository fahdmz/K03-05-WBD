package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"

	"github.com/fahdmz/K03-05-WBD/internal/web"
)

type sessionKey struct{}

func sessionFromContext(ctx context.Context) (*Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(*Session)
	return s, ok
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func (s *Service) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.cfg.CookieName)
		if err != nil {
			web.WriteError(w, http.StatusUnauthorized, "belum login")
			return
		}

		sess, err := s.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			if errors.Is(err, ErrUnauthenticated) {
				s.clearCookie(w)
				web.WriteError(w, http.StatusUnauthorized, "sesi tidak valid")
				return
			}
			log.Printf("auth: authenticate failed: %v", err)
			web.WriteError(w, http.StatusInternalServerError, "terjadi kesalahan pada server")
			return
		}

		if sess.NewToken != "" {
			s.setCookie(w, sess.NewToken)
		}

		if !isSafeMethod(r.Method) {
			got := []byte(r.Header.Get("X-CSRF-Token"))
			if subtle.ConstantTimeCompare(got, []byte(sess.CSRFToken)) != 1 {
				web.WriteError(w, http.StatusForbidden, "token CSRF tidak valid")
				return
			}
		}

		ctx := WithUser(r.Context(), &sess.User)
		ctx = context.WithValue(ctx, sessionKey{}, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

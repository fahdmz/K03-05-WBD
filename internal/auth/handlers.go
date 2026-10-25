package auth

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/fahdmz/K03-05-WBD/internal/web"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type userDTO struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"isAdmin"`
	IsActive bool   `json:"isActive"`
}

type sessionResponse struct {
	User      userDTO `json:"user"`
	CSRFToken string  `json:"csrfToken"`
}

func toSessionResponse(sess *Session) sessionResponse {
	return sessionResponse{
		User: userDTO{
			ID:       sess.User.ID,
			Username: sess.User.Username,
			IsAdmin:  sess.User.IsAdmin,
			IsActive: true,
		},
		CSRFToken: sess.CSRFToken,
	}
}

func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", s.loginHandler)
	mux.Handle("POST /api/auth/logout", s.Protect(http.HandlerFunc(s.logoutHandler)))
	mux.Handle("GET /api/auth/me", s.Protect(http.HandlerFunc(s.meHandler)))
}

func (s *Service) loginHandler(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		web.WriteError(w, http.StatusBadRequest, "format request tidak valid")
		return
	}
	if req.Username == "" || req.Password == "" {
		web.WriteError(w, http.StatusBadRequest, "username dan password wajib diisi")
		return
	}

	if old, err := r.Cookie(s.cfg.CookieName); err == nil {
		_ = s.Logout(r.Context(), old.Value)
	}

	token, sess, err := s.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			web.WriteError(w, http.StatusUnauthorized, "username atau password salah")
			return
		}
		log.Printf("auth: login failed: %v", err)
		web.WriteError(w, http.StatusInternalServerError, "terjadi kesalahan pada server")
		return
	}

	s.setCookie(w, token)
	web.WriteJSON(w, http.StatusOK, toSessionResponse(sess))
}

func (s *Service) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(s.cfg.CookieName); err == nil {
		if err := s.Logout(r.Context(), cookie.Value); err != nil {
			log.Printf("auth: logout failed: %v", err)
			web.WriteError(w, http.StatusInternalServerError, "terjadi kesalahan pada server")
			return
		}
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) meHandler(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFromContext(r.Context())
	if !ok {
		web.WriteError(w, http.StatusUnauthorized, "belum login")
		return
	}
	web.WriteJSON(w, http.StatusOK, toSessionResponse(sess))
}

package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/dbmanager"
)

func (h *ResourceHandler) TestDatabase(
	w http.ResponseWriter,
	r *http.Request,
) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	var input struct {
		Host     string `json:"host"`
		Port     int    `json:"port"`
		DBName   string `json:"dbname"`
		Username string `json:"username"`
		Password string `json:"password"`
		SSLMode  string `json:"sslmode"`
	}
	input.Port = 5432
	input.SSLMode = "disable"

	if !decodeUserBody(w, r, &input) {
		return
	}

	config := databaseResponse{
		Name:     "connection-test",
		Host:     input.Host,
		Port:     input.Port,
		DBName:   input.DBName,
		Username: input.Username,
		SSLMode:  input.SSLMode,
	}
	if !validConnection(config) {
		resourceFailure(w, badInput("Konfigurasi database tidak valid"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	err := h.manager.Test(ctx, dbmanager.ConnConfig{
		Host:     input.Host,
		Port:     input.Port,
		DBName:   input.DBName,
		Username: input.Username,
		Password: input.Password,
		SSLMode:  input.SSLMode,
	})
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "Koneksi database gagal",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{
		"ok": true,
	})
}
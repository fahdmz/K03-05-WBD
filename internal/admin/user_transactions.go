package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type userTransactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

func (h *Handler) WithSessionManagerFactory(
	factory SessionManagerFactory,
) *Handler {
	copy := *h
	copy.sessionFactory = factory
	return &copy
}

func (h *Handler) sessionManager(tx pgx.Tx) (SessionManager, error) {
	if h.sessionFactory == nil {
		return nil, &resourceError{
			status:  http.StatusServiceUnavailable,
			message: "Pengelolaan session belum tersedia",
		}
	}

	manager := h.sessionFactory(tx)
	if manager == nil {
		return nil, errors.New("session manager factory returned nil")
	}
	return manager, nil
}

func (h *Handler) runUserTransaction(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	action func(context.Context, pgx.Tx) (any, error),
) {
	beginner, ok := h.db.(userTransactionBeginner)
	if !ok {
		writeUserError(w, errors.New("database does not support transactions"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	tx, err := beginner.Begin(ctx)
	if err != nil {
		writeUserError(w, err)
		return
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()

	result, err := action(ctx, tx)
	if err != nil {
		var inputErr *resourceError
		if errors.As(err, &inputErr) {
			writeJSON(w, inputErr.status, map[string]string{
				"error": inputErr.message,
			})
		} else {
			writeUserError(w, err)
		}
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeUserError(w, err)
		return
	}

	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, result)
}

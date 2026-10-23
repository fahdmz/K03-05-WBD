package admin

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type groupResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func scanGroup(row pgx.Row) (groupResponse, error) {
	var group groupResponse
	err := row.Scan(&group.ID, &group.Name, &group.Description)
	return group, err
}

func (h *ResourceHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		rows, err := tx.Query(ctx, `
			SELECT id, name, description FROM groups ORDER BY id
		`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		groups := make([]groupResponse, 0)
		for rows.Next() {
			group, err := scanGroup(rows)
			if err != nil {
				return nil, err
			}
			groups = append(groups, group)
		}
		return groups, rows.Err()
	}, nil)
}

func (h *ResourceHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !decodeUserBody(w, r, &input) {
		return
	}

	input.Name = strings.TrimSpace(input.Name)
	if !validResourceName(input.Name) ||
		utf8.RuneCountInString(input.Description) > 2000 ||
		strings.ContainsRune(input.Description, '\x00') {
		resourceFailure(w, badInput("Nama atau deskripsi group tidak valid"))
		return
	}

	h.run(w, r, http.StatusCreated, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return scanGroup(tx.QueryRow(ctx, `
			INSERT INTO groups (name, description)
			VALUES ($1, $2)
			RETURNING id, name, description
		`, input.Name, input.Description))
	}, nil)
}

func (h *ResourceHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminUser(w, r); !ok {
		return
	}

	id, err := resourceID(r)
	if err != nil {
		resourceFailure(w, err)
		return
	}

	var input struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if !decodeUserBody(w, r, &input) {
		return
	}

	if input.Name == nil && input.Description == nil {
		resourceFailure(w, badInput("Kirim minimal satu field"))
		return
	}

	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if !validResourceName(name) {
			resourceFailure(w, badInput("Nama group tidak valid"))
			return
		}
		input.Name = &name
	}

	if input.Description != nil &&
		(utf8.RuneCountInString(*input.Description) > 2000 ||
			strings.ContainsRune(*input.Description, '\x00')) {
		resourceFailure(w, badInput("Deskripsi group tidak valid"))
		return
	}

	h.run(w, r, http.StatusOK, func(ctx context.Context, tx pgx.Tx) (any, error) {
		return scanGroup(tx.QueryRow(ctx, `
			UPDATE groups
			SET name = COALESCE($2::text, name),
			    description = COALESCE($3::text, description)
			WHERE id = $1
			RETURNING id, name, description
		`, id, input.Name, input.Description))
	}, nil)
}

func (h *ResourceHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, http.StatusNoContent, func(ctx context.Context, tx pgx.Tx) (any, error) {
		id, err := resourceID(r)
		if err != nil {
			return nil, err
		}
		if err := lockGroup(ctx, tx, id); err != nil {
			return nil, err
		}
		if err := revokeGroupSessions(ctx, tx, id); err != nil {
			return nil, err
		}

		_, err = tx.Exec(ctx, "DELETE FROM groups WHERE id = $1", id)
		return nil, err
	}, nil)
}
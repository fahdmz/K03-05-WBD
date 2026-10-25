package dbmanager

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/fahdmz/K03-05-WBD/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxPageSize int64 = 100

type Viewer struct {
	platform   *pgxpool.Pool
	manager    Manager
	authorizer authz.Authorizer
}

func NewViewer(platform *pgxpool.Pool, manager Manager, authorizer authz.Authorizer) *Viewer {
	return &Viewer{platform: platform, manager: manager, authorizer: authorizer}
}

func (v *Viewer) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/databases", v.listDatabases)
	mux.HandleFunc("GET /api/databases/{id}/schemas", v.listSchemas)
	mux.HandleFunc("GET /api/databases/{id}/schemas/{schema}/tables", v.listTables)
	mux.HandleFunc("GET /api/databases/{id}/schemas/{schema}/tables/{table}", v.tableDetails)
	mux.HandleFunc("GET /api/databases/{id}/schemas/{schema}/tables/{table}/rows", v.tableRows)
}

type databaseSummary struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Level string `json:"level"`
}

func (v *Viewer) listDatabases(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok || u == nil {
		web.WriteError(w, http.StatusUnauthorized, "Belum login")
		return
	}
	if v.platform == nil || v.authorizer == nil {
		web.WriteError(w, http.StatusInternalServerError, "Viewer belum dikonfigurasi")
		return
	}

	rows, err := v.platform.Query(r.Context(), `
		SELECT id, name
		FROM db_connections
		ORDER BY name`)
	if err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar database")
		return
	}
	defer rows.Close()

	result := make([]databaseSummary, 0)
	for rows.Next() {
		var item databaseSummary
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar database")
			return
		}
		level, err := v.authorizer.EffectiveLevel(r.Context(), u.ID, item.ID)
		if err != nil {
			web.WriteError(w, http.StatusInternalServerError, "Gagal memeriksa akses database")
			return
		}
		switch level {
		case authz.View:
			item.Level = "view"
		case authz.Edit:
			item.Level = "edit"
		default:
			continue
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		web.WriteError(w, http.StatusInternalServerError, "Gagal membaca daftar database")
		return
	}
	web.WriteJSON(w, http.StatusOK, result)
}

func (v *Viewer) listSchemas(w http.ResponseWriter, r *http.Request) {
	pool, ok := v.authorizedPool(w, r, authz.View)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('pg_catalog', 'information_schema')
		  AND schema_name NOT LIKE 'pg_toast%'
		ORDER BY schema_name`)
	if err != nil {
		writeTargetError(w, err)
		return
	}
	defer rows.Close()

	schemas := make([]map[string]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			writeTargetError(w, err)
			return
		}
		schemas = append(schemas, map[string]string{"name": name})
	}
	if err := rows.Err(); err != nil {
		writeTargetError(w, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, schemas)
}

func (v *Viewer) listTables(w http.ResponseWriter, r *http.Request) {
	pool, schema, ok := v.authorizedSchemaPool(w, r)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = $1 AND table_type = 'BASE TABLE'
		ORDER BY table_name`, schema)
	if err != nil {
		writeTargetError(w, err)
		return
	}
	defer rows.Close()

	tables := make([]map[string]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			writeTargetError(w, err)
			return
		}
		tables = append(tables, map[string]string{"name": name})
	}
	if err := rows.Err(); err != nil {
		writeTargetError(w, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, tables)
}

type tableColumn struct {
	Name     string  `json:"name"`
	DataType string  `json:"dataType"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
	Position int     `json:"position"`
}

type tableConstraint struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Definition string `json:"definition"`
}

type tableIndex struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

type tableDetailsResponse struct {
	Schema      string            `json:"schema"`
	Name        string            `json:"name"`
	Columns     []tableColumn     `json:"columns"`
	Constraints []tableConstraint `json:"constraints"`
	Indexes     []tableIndex      `json:"indexes"`
}

func (v *Viewer) tableDetails(w http.ResponseWriter, r *http.Request) {
	pool, schema, table, ok := v.authorizedTablePool(w, r)
	if !ok {
		return
	}
	if !tableExists(r, pool, schema, table, w) {
		return
	}

	result := tableDetailsResponse{
		Schema: schema, Name: table,
		Columns:     make([]tableColumn, 0),
		Constraints: make([]tableConstraint, 0),
		Indexes:     make([]tableIndex, 0),
	}
	columnRows, err := pool.Query(r.Context(), `
		SELECT column_name, data_type, is_nullable, column_default, ordinal_position
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position`, schema, table)
	if err != nil {
		writeTargetError(w, err)
		return
	}
	for columnRows.Next() {
		var column tableColumn
		var nullable string
		if err := columnRows.Scan(&column.Name, &column.DataType, &nullable, &column.Default, &column.Position); err != nil {
			columnRows.Close()
			writeTargetError(w, err)
			return
		}
		column.Nullable = nullable == "YES"
		result.Columns = append(result.Columns, column)
	}
	if err := columnRows.Err(); err != nil {
		columnRows.Close()
		writeTargetError(w, err)
		return
	}
	columnRows.Close()

	constraintRows, err := pool.Query(r.Context(), `
		SELECT c.conname, c.contype::text, pg_get_constraintdef(c.oid)
		FROM pg_catalog.pg_constraint c
		JOIN pg_catalog.pg_class t ON t.oid = c.conrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = $1 AND t.relname = $2
		ORDER BY c.conname`, schema, table)
	if err != nil {
		writeTargetError(w, err)
		return
	}
	for constraintRows.Next() {
		var constraint tableConstraint
		if err := constraintRows.Scan(&constraint.Name, &constraint.Type, &constraint.Definition); err != nil {
			constraintRows.Close()
			writeTargetError(w, err)
			return
		}
		result.Constraints = append(result.Constraints, constraint)
	}
	if err := constraintRows.Err(); err != nil {
		constraintRows.Close()
		writeTargetError(w, err)
		return
	}
	constraintRows.Close()

	indexRows, err := pool.Query(r.Context(), `
		SELECT indexname, indexdef
		FROM pg_catalog.pg_indexes
		WHERE schemaname = $1 AND tablename = $2
		ORDER BY indexname`, schema, table)
	if err != nil {
		writeTargetError(w, err)
		return
	}
	defer indexRows.Close()
	for indexRows.Next() {
		var index tableIndex
		if err := indexRows.Scan(&index.Name, &index.Definition); err != nil {
			writeTargetError(w, err)
			return
		}
		result.Indexes = append(result.Indexes, index)
	}
	if err := indexRows.Err(); err != nil {
		writeTargetError(w, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, result)
}

type tableRowsResponse struct {
	Columns  []string         `json:"columns"`
	Rows     []map[string]any `json:"rows"`
	Page     int64            `json:"page"`
	PageSize int64            `json:"pageSize"`
	Total    int64            `json:"total"`
}

func (v *Viewer) tableRows(w http.ResponseWriter, r *http.Request) {
	pool, schema, table, ok := v.authorizedTablePool(w, r)
	if !ok {
		return
	}
	page, pageSize, offset, err := parsePagination(r)
	if err != nil {
		web.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !tableExists(r, pool, schema, table, w) {
		return
	}

	qualified := pgx.Identifier{schema, table}.Sanitize()
	var total int64
	if err := pool.QueryRow(r.Context(), "SELECT count(*) FROM "+qualified).Scan(&total); err != nil {
		writeTargetError(w, err)
		return
	}
	rows, err := pool.Query(r.Context(), fmt.Sprintf("SELECT * FROM %s LIMIT $1 OFFSET $2", qualified), pageSize, offset)
	if err != nil {
		writeTargetError(w, err)
		return
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	result := tableRowsResponse{
		Columns: make([]string, len(fields)),
		Rows:    make([]map[string]any, 0),
		Page:    page, PageSize: pageSize, Total: total,
	}
	for i, field := range fields {
		result.Columns[i] = field.Name
	}
	for rows.Next() {
		values := make([]any, len(fields))
		destinations := make([]any, len(fields))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			writeTargetError(w, err)
			return
		}
		item := make(map[string]any, len(fields))
		for i, field := range fields {
			item[field.Name] = values[i]
		}
		result.Rows = append(result.Rows, item)
	}
	if err := rows.Err(); err != nil {
		writeTargetError(w, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, result)
}

func (v *Viewer) authorizedPool(w http.ResponseWriter, r *http.Request, min authz.Level) (*pgxpool.Pool, bool) {
	dbID, err := parseDatabaseID(r)
	if err != nil {
		web.WriteError(w, http.StatusBadRequest, "ID database tidak valid")
		return nil, false
	}
	u, ok := auth.UserFromContext(r.Context())
	if !ok || u == nil {
		web.WriteError(w, http.StatusUnauthorized, "Belum login")
		return nil, false
	}
	if v.authorizer == nil || v.manager == nil {
		web.WriteError(w, http.StatusInternalServerError, "Viewer belum dikonfigurasi")
		return nil, false
	}
	if err := v.authorizer.Require(r.Context(), u.ID, dbID, min); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			web.WriteError(w, http.StatusForbidden, "Tidak punya akses ke database ini")
		} else {
			web.WriteError(w, http.StatusInternalServerError, "Gagal memeriksa akses database")
		}
		return nil, false
	}
	pool, err := v.manager.Pool(r.Context(), dbID)
	if err != nil {
		if errors.Is(err, ErrDatabaseNotFound) {
			web.WriteError(w, http.StatusNotFound, "Database tidak ditemukan")
		} else {
			web.WriteError(w, http.StatusBadGateway, "Koneksi ke database target gagal")
		}
		return nil, false
	}
	return pool, true
}

func (v *Viewer) authorizedSchemaPool(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, string, bool) {
	pool, ok := v.authorizedPool(w, r, authz.View)
	if !ok {
		return nil, "", false
	}
	schema := r.PathValue("schema")
	if schema == "" {
		web.WriteError(w, http.StatusBadRequest, "Schema tidak valid")
		return nil, "", false
	}
	return pool, schema, true
}

func (v *Viewer) authorizedTablePool(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, string, string, bool) {
	pool, schema, ok := v.authorizedSchemaPool(w, r)
	if !ok {
		return nil, "", "", false
	}
	table := r.PathValue("table")
	if table == "" {
		web.WriteError(w, http.StatusBadRequest, "Table tidak valid")
		return nil, "", "", false
	}
	return pool, schema, table, true
}

func tableExists(r *http.Request, pool *pgxpool.Pool, schema, table string, w http.ResponseWriter) bool {
	var exists bool
	err := pool.QueryRow(r.Context(), `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = $1 AND table_name = $2 AND table_type = 'BASE TABLE'
		)`, schema, table).Scan(&exists)
	if err != nil {
		writeTargetError(w, err)
		return false
	}
	if !exists {
		web.WriteError(w, http.StatusNotFound, "Table tidak ditemukan")
		return false
	}
	return true
}

func parseDatabaseID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, errors.New("invalid database id")
	}
	return id, nil
}

func parsePagination(r *http.Request) (page, pageSize, offset int64, err error) {
	page = 1
	pageSize = 50
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || page < 1 {
			return 0, 0, 0, errors.New("page harus berupa bilangan positif")
		}
	}
	if raw := r.URL.Query().Get("pageSize"); raw != "" {
		pageSize, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || pageSize < 1 || pageSize > maxPageSize {
			return 0, 0, 0, fmt.Errorf("pageSize harus antara 1 dan %d", maxPageSize)
		}
	}
	if page-1 > math.MaxInt64/pageSize {
		return 0, 0, 0, errors.New("page terlalu besar")
	}
	offset = (page - 1) * pageSize
	return page, pageSize, offset, nil
}

func writeTargetError(w http.ResponseWriter, err error) {
	_ = err // Detail error target ga dikirim ke client
	web.WriteError(w, http.StatusBadGateway, "Koneksi ke database target gagal")
}

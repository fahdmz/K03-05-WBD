package workflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/auth"
	"github.com/fahdmz/K03-05-WBD/internal/authz"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type stubAuthorizer struct{ err error }

func (a stubAuthorizer) EffectiveLevel(context.Context, int64, int64) (authz.Level, error) {
	if a.err != nil {
		return authz.None, a.err
	}
	return authz.Edit, nil
}

func (a stubAuthorizer) Require(context.Context, int64, int64, authz.Level) error { return a.err }

func TestWorkflowValidationUnit(t *testing.T) {
	h := NewHandler(nil, stubAuthorizer{})
	mux := http.NewServeMux()
	h.Register(mux)

	asUser := func(req *http.Request) *http.Request {
		return req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: 1, Username: "leader"}))
	}

	t.Run("unauthenticated requests return 401", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/api/reviews"},
			{http.MethodGet, "/api/change-requests/1"},
			{http.MethodPost, "/api/change-requests/1/approve"},
			{http.MethodPost, "/api/change-requests/1/reject"},
			{http.MethodPost, "/api/change-requests/1/run"},
		} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s status = %d, want 401", tc.method, tc.path, rec.Code)
			}
		}
	})

	t.Run("invalid path id returns 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/change-requests/abc", nil)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("reject without comment returns 400", func(t *testing.T) {
		hCfg := &Handler{platform: &pgxpool.Pool{}, authorizer: stubAuthorizer{}}
		cfgMux := http.NewServeMux()
		hCfg.Register(cfgMux)

		for _, body := range []string{``, `{}`, `{"comment":"   "}`} {
			rec := httptest.NewRecorder()
			cfgMux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, "/api/change-requests/1/reject", strings.NewReader(body))))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("body=%q status = %d, want 400; resp=%s", body, rec.Code, rec.Body.String())
			}
		}
	})

	t.Run("invalid status filter returns 400", func(t *testing.T) {
		hCfg := &Handler{platform: &pgxpool.Pool{}, authorizer: stubAuthorizer{}}
		cfgMux := http.NewServeMux()
		hCfg.Register(cfgMux)

		rec := httptest.NewRecorder()
		cfgMux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodGet, "/api/databases/1/change-requests?status=bogus", nil)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; resp=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("comment over 2000 runes returns 400", func(t *testing.T) {
		hCfg := &Handler{platform: &pgxpool.Pool{}, authorizer: stubAuthorizer{}}
		cfgMux := http.NewServeMux()
		hCfg.Register(cfgMux)

		body := fmt.Sprintf(`{"comment":"%s"}`, strings.Repeat("x", 2001))
		rec := httptest.NewRecorder()
		cfgMux.ServeHTTP(rec, asUser(httptest.NewRequest(http.MethodPost, "/api/change-requests/1/approve", strings.NewReader(body))))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}

func setupWorkflowTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://test:test@localhost:5434/test?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	control, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("test-db tidak bisa dihubungi: %v", err)
	}
	if err := control.Ping(ctx); err != nil {
		control.Close()
		t.Skipf("test-db tidak bisa dihubungi, jalankan docker compose up -d test-db: %v", err)
	}
	t.Cleanup(control.Close)

	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	schemaName := "workflow_test_" + hex.EncodeToString(buf)
	quoted := pgx.Identifier{schemaName}.Sanitize()

	if _, err := control.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()
		_, _ = control.Exec(cleanCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	for _, file := range []string{"../../migrations/001_init.sql", "../../migrations/002_m2.sql"} {
		sqlBytes, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
	}
	return pool
}

func TestWorkflowIntegration(t *testing.T) {
	pool := setupWorkflowTestDB(t)
	ctx := context.Background()

	// Seed users:
	// 10 = dev (edit, leader group A)
	// 20 = viewerLeader (view, leader group B)
	// 30 = memberOnly (anggota group A, bukan leader)
	// 40 = outsiderLeader (leader group C yang belum punya akses ke DB 10)
	_, err := pool.Exec(ctx, `
		INSERT INTO users (id, username, password_hash, is_active) VALUES
			(10, 'dev', 'x', TRUE),
			(20, 'viewer_leader', 'x', TRUE),
			(30, 'member_only', 'x', TRUE),
			(40, 'outsider_leader', 'x', TRUE);

		INSERT INTO db_connections (id, name, host, port, dbname, username, password_encrypted)
		VALUES (10, 'app-db', 'localhost', 5433, 'app', 'app', '\x00');

		INSERT INTO groups (id, name, leader_user_id) VALUES
			(10, 'group-edit', 10),
			(20, 'group-view', 20),
			(30, 'group-extra', 40);

		INSERT INTO group_members (group_id, user_id) VALUES
			(10, 10),
			(10, 30),
			(20, 20),
			(30, 40);

		INSERT INTO group_db_access (group_id, db_connection_id, level) VALUES
			(10, 10, 'edit'),
			(20, 10, 'view');
	`)
	if err != nil {
		t.Fatalf("seed fixtures: %v", err)
	}

	authorizer := authz.NewPostgresAuthorizer(pool)
	handler := NewHandler(pool, authorizer)
	mux := http.NewServeMux()
	handler.Register(mux)

	reqAs := func(userID int64, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: userID}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	createSubmittedCR := func(t *testing.T, title string) (int64, int64, int64) {
		t.Helper()
		var migID, revID int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO migrations (db_connection_id, title, sql, status, created_by)
			VALUES (10, $1, 'CREATE TABLE t (id INT);', 'draft', 10) RETURNING id
		`, title).Scan(&migID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `
			INSERT INTO migration_revisions (migration_id, revision_no, status, sql_up, sql_down, mode, created_by, submitted_at)
			VALUES ($1, 1, 'submitted', 'CREATE TABLE t (id INT);', 'DROP TABLE t;', 'normal', 10, now()) RETURNING id
		`, migID).Scan(&revID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE migrations SET current_revision_id = $2 WHERE id = $1`, migID, revID); err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		crID, err := CreateRequest(ctx, tx, RequestInput{
			DatabaseID:  10,
			MigrationID: migID,
			RevisionID:  revID,
			Action:      "apply",
			RequestedBy: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return migID, revID, crID
	}

	t.Run("approval flow, dynamic access change, run 202, and revalidate", func(t *testing.T) {
		_, _, crID := createSubmittedCR(t, "mig-1")
		crPath := fmt.Sprintf("/api/change-requests/%d", crID)

		// 1. Non-leader dan leader group tanpa akses DB ditolak 403.
		if rec := reqAs(30, http.MethodPost, crPath+"/approve", `{}`); rec.Code != http.StatusForbidden {
			t.Fatalf("non-leader approve status = %d, want 403", rec.Code)
		}
		if rec := reqAs(40, http.MethodPost, crPath+"/approve", `{}`); rec.Code != http.StatusForbidden {
			t.Fatalf("outsider leader approve status = %d, want 403", rec.Code)
		}

		// 2. Run sebelum approved ditolak 400.
		if rec := reqAs(10, http.MethodPost, crPath+"/run", ``); rec.Code != http.StatusBadRequest {
			t.Fatalf("run before approved status = %d, want 400", rec.Code)
		}

		// 3. GET /api/reviews untuk leader 10 memuat crID.
		revRec := reqAs(10, http.MethodGet, "/api/reviews", ``)
		if revRec.Code != http.StatusOK {
			t.Fatalf("reviews status = %d, want 200", revRec.Code)
		}
		var reviews []ReviewItem
		if err := json.Unmarshal(revRec.Body.Bytes(), &reviews); err != nil {
			t.Fatal(err)
		}
		if len(reviews) != 1 || reviews[0].ID != crID {
			t.Fatalf("unexpected reviews for leader 10: %#v", reviews)
		}

		// 4. Leader group 10 approve -> belum approved karena group 20 belum approve.
		rec := reqAs(10, http.MethodPost, crPath+"/approve", `{"comment":"ok dari edit"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("leader 10 approve status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var detail ChangeRequestDetail
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Status != "pending_review" || detail.Approval.Approved {
			t.Fatalf("expected pending_review after 1/2 approvals, got %s approved=%v", detail.Status, detail.Approval.Approved)
		}

		// 5. Leader group 20 approve -> status naik menjadi approved.
		rec = reqAs(20, http.MethodPost, crPath+"/approve", `{"comment":"ok dari view"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("leader 20 approve status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Status != "approved" || !detail.Approval.Approved {
			t.Fatalf("expected approved after 2/2 approvals, got %s approved=%v", detail.Status, detail.Approval.Approved)
		}

		// 6. Perubahan akses dinamis: group 30 baru diberi akses view ke DB 10 -> run ditolak 409 & balik ke pending_review.
		if _, err := pool.Exec(ctx, `INSERT INTO group_db_access (group_id, db_connection_id, level) VALUES (30, 10, 'view')`); err != nil {
			t.Fatal(err)
		}
		if rec := reqAs(10, http.MethodPost, crPath+"/run", ``); rec.Code != http.StatusConflict {
			t.Fatalf("run after new group added status = %d, want 409; body=%s", rec.Code, rec.Body.String())
		}

		// 7. Leader group 30 (user 40) approve -> kembali approved.
		rec = reqAs(40, http.MethodPost, crPath+"/approve", `{"comment":"ok dari extra"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("leader 40 approve status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}

		// 8. View-only user (20) ditolak 403 saat run.
		if rec := reqAs(20, http.MethodPost, crPath+"/run", ``); rec.Code != http.StatusForbidden {
			t.Fatalf("view-only run status = %d, want 403", rec.Code)
		}

		// 9. User edit (10) klik run -> 202 Accepted + jobId & executionId.
		runRec := reqAs(10, http.MethodPost, crPath+"/run", ``)
		if runRec.Code != http.StatusAccepted {
			t.Fatalf("run status = %d, want 202; body=%s", runRec.Code, runRec.Body.String())
		}
		var runResp struct {
			JobID       int64  `json:"jobId"`
			ExecutionID string `json:"executionId"`
		}
		if err := json.Unmarshal(runRec.Body.Bytes(), &runResp); err != nil {
			t.Fatal(err)
		}
		if runResp.JobID <= 0 || runResp.ExecutionID == "" {
			t.Fatalf("unexpected run response: %#v", runResp)
		}

		// 10. Double run ditolak 409.
		if rec := reqAs(10, http.MethodPost, crPath+"/run", ``); rec.Code != http.StatusConflict {
			t.Fatalf("double run status = %d, want 409", rec.Code)
		}

		// 11. Revalidate lolos saat semua kondisi masih valid.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := Revalidate(ctx, tx, crID); err != nil {
			tx.Rollback(ctx)
			t.Fatalf("expected Revalidate to succeed, got: %v", err)
		}
		tx.Rollback(ctx)

		// 12. Revalidate gagal dengan pesan jelas jika leader group 30 dikosongkan setelah antre.
		if _, err := pool.Exec(ctx, `UPDATE groups SET leader_user_id = NULL WHERE id = 30`); err != nil {
			t.Fatal(err)
		}
		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := Revalidate(ctx, tx, crID); err == nil || !strings.Contains(err.Error(), "group-extra") {
			tx.Rollback(ctx)
			t.Fatalf("expected Revalidate error mentioning group-extra, got: %v", err)
		}
		tx.Rollback(ctx)

		// Kembalikan leader group 30 lalu hapus akses group 30 agar bersih untuk subtest berikutnya.
		_, _ = pool.Exec(ctx, `UPDATE groups SET leader_user_id = 40 WHERE id = 30; DELETE FROM group_db_access WHERE group_id = 30;`)
	})

	t.Run("one rejection sets status to rejected", func(t *testing.T) {
		_, _, crID := createSubmittedCR(t, "mig-reject")
		crPath := fmt.Sprintf("/api/change-requests/%d", crID)

		rec := reqAs(20, http.MethodPost, crPath+"/reject", `{"comment":"berisiko untuk service B"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("reject status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var detail ChangeRequestDetail
		if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Status != "rejected" || !detail.Approval.Rejected || detail.Approval.Approved {
			t.Fatalf("unexpected rejected detail: status=%s approval=%#v", detail.Status, detail.Approval)
		}
		if rec := reqAs(10, http.MethodPost, crPath+"/run", ``); rec.Code != http.StatusBadRequest {
			t.Fatalf("run rejected CR status = %d, want 400", rec.Code)
		}
	})

	t.Run("multi-step resume chain passes Revalidate", func(t *testing.T) {
		migID, revID, crP := createSubmittedCR(t, "mig-chain")

		// Approve & jalankan P -> simulasi berhenti partial.
		reqAs(10, http.MethodPost, fmt.Sprintf("/api/change-requests/%d/approve", crP), `{}`)
		reqAs(20, http.MethodPost, fmt.Sprintf("/api/change-requests/%d/approve", crP), `{}`)
		runP := reqAs(10, http.MethodPost, fmt.Sprintf("/api/change-requests/%d/run", crP), ``)
		if runP.Code != http.StatusAccepted {
			t.Fatalf("run P status = %d, want 202; body=%s", runP.Code, runP.Body.String())
		}
		var respP struct {
			ExecutionID string `json:"executionId"`
		}
		if err := json.Unmarshal(runP.Body.Bytes(), &respP); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'partial' WHERE change_request_id = $1`, crP); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE change_requests SET status = 'partial' WHERE id = $1`, crP); err != nil {
			t.Fatal(err)
		}

		// Buat resume R1 dari P -> approve & jalankan -> berhenti partial lagi.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		crR1, err := CreateRequest(ctx, tx, RequestInput{
			DatabaseID:        10,
			MigrationID:       migID,
			RevisionID:        revID,
			Action:            "resume",
			TargetExecutionID: &respP.ExecutionID,
			RequestedBy:       10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		reqAs(10, http.MethodPost, fmt.Sprintf("/api/change-requests/%d/approve", crR1), `{}`)
		reqAs(20, http.MethodPost, fmt.Sprintf("/api/change-requests/%d/approve", crR1), `{}`)
		runR1 := reqAs(10, http.MethodPost, fmt.Sprintf("/api/change-requests/%d/run", crR1), ``)
		if runR1.Code != http.StatusAccepted {
			t.Fatalf("run R1 status = %d, want 202; body=%s", runR1.Code, runR1.Body.String())
		}
		var respR1 struct {
			ExecutionID string `json:"executionId"`
		}
		if err := json.Unmarshal(runR1.Body.Bytes(), &respR1); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'partial' WHERE change_request_id = $1`, crR1); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE change_requests SET status = 'partial' WHERE id = $1`, crR1); err != nil {
			t.Fatal(err)
		}

		// Buat resume R2 dari R1 -> approve & jalankan -> Revalidate harus LOLOS walau P dan R1 masih partial.
		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		crR2, err := CreateRequest(ctx, tx, RequestInput{
			DatabaseID:        10,
			MigrationID:       migID,
			RevisionID:        revID,
			Action:            "resume",
			TargetExecutionID: &respR1.ExecutionID,
			RequestedBy:       10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		reqAs(10, http.MethodPost, fmt.Sprintf("/api/change-requests/%d/approve", crR2), `{}`)
		reqAs(20, http.MethodPost, fmt.Sprintf("/api/change-requests/%d/approve", crR2), `{}`)
		runR2 := reqAs(10, http.MethodPost, fmt.Sprintf("/api/change-requests/%d/run", crR2), ``)
		if runR2.Code != http.StatusAccepted {
			t.Fatalf("run R2 status = %d, want 202; body=%s", runR2.Code, runR2.Body.String())
		}

		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := Revalidate(ctx, tx, crR2); err != nil {
			tx.Rollback(ctx)
			t.Fatalf("expected Revalidate on 2nd resume in chain to succeed, got: %v", err)
		}
		tx.Rollback(ctx)
	})
}

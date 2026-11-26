package worker

import (
	"context"
	"time"

	"github.com/fahdmz/K03-05-WBD/internal/history"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	leaseFor  = 30 * time.Second
	heartbeat = 10 * time.Second
)

type job struct {
	ID          int64
	CRID        int64
	DBID        int64
	ExecID      string
	PrevStatus  string // status sebelum di-claim: queued / uncertain / running (lease habis)
	Attempt     int
	MaxAttempts int
	Trace       map[string]string
	First       bool    // claim pertama
	Progress    int     // batch committed dari attempt sebelumnya
	Waited      float64 // detik sejak job dibikin
}

// careful: hasil attempt sebelumnya mungkin udah ada di target (recovery, atau batch yang
// udah punya batch committed). Job kayak gini nggak boleh di-failed/requeue sebelum bukti kebaca,
// dan nggak divalidasi ulang (bukti di target yang nentuin).
func (j job) careful() bool {
	return j.touchedUnsure() || j.Progress > 0
}

// touchedUnsure: hasil attempt sebelumnya belum dicek (recovery), jadi balik antre tetep uncertain
func (j job) touchedUnsure() bool {
	return j.Attempt > 0 && (j.PrevStatus == "uncertain" || j.PrevStatus == "running")
}

// run = bekal executor buat satu eksekusi
type run struct {
	job        job
	plan       history.Plan
	root       string         // root execution ID, dipake di _platform & history
	rootAction history.Action // apply / forward / rollback
	rootTarget *string        // kalau root rollback: eksekusi yang dibatalin
	attempt    int            // attempt yang lagi jalan (udah ditambah)
	target     *pgxpool.Pool
	platform   *pgxpool.Pool
	progress   func(ctx context.Context, batches int, rows int64) // update jobs, best effort
	onBatch    func(rows int64)                                   // metrics per batch yang commit, boleh nil
}

func (r *run) dir() string {
	return string(r.plan.Direction)
}

type outcomeKind int

const (
	okDone      outcomeKind = iota // commit sukses
	okRetry                        // error sementara, belum ada yang commit di attempt ini
	okFail                         // error permanen
	okPartial                      // batch berhenti, udah ada batch committed
	okUncertain                    // hasil commit belum pasti
	okAbort                        // ctx dibatalin (lock ilang / shutdown): job dibiarin, nanti di-recover
)

func (k outcomeKind) String() string {
	switch k {
	case okDone:
		return "success"
	case okRetry:
		return "retry"
	case okFail:
		return "failed"
	case okPartial:
		return "partial"
	case okUncertain:
		return "uncertain"
	case okAbort:
		return "abort"
	}
	return "unknown"
}

type outcome struct {
	kind        outcomeKind
	err         error
	sqlstate    string
	executedSQL string
	batches     int   // total batch committed buat root+arah
	rows        int64 // total row
	startedAt   time.Time
	committedAt time.Time
}

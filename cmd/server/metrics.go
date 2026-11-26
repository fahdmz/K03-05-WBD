package main

import (
	"github.com/fahdmz/K03-05-WBD/internal/metrics"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newMetrics(db *pgxpool.Pool) *metrics.Registry {
	reg := metrics.New()
	reg.GaugeFunc("server_db_pool_connections", "Koneksi pool database platform di server.", func() []metrics.Sample {
		s := db.Stat()
		return []metrics.Sample{
			{Labels: []string{"active"}, Value: float64(s.AcquiredConns())},
			{Labels: []string{"idle"}, Value: float64(s.IdleConns())},
			{Labels: []string{"total"}, Value: float64(s.TotalConns())},
			{Labels: []string{"max"}, Value: float64(s.MaxConns())},
		}
	}, "state")
	reg.CounterFunc("server_db_pool_acquire_wait_seconds_total",
		"Total waktu nunggu dapet koneksi dari pool platform.", func() []metrics.Sample {
			return []metrics.Sample{{Value: db.Stat().AcquireDuration().Seconds()}}
		})
	return reg
}

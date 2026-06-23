// Package metrics expone un handler HTTP con estadísticas básicas del proceso.
// No usa librerías externas (Prometheus, etc.) para mantener la dependencia mínima.
package metrics

import (
	"encoding/json"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	startTime     = time.Now()
	requestsTotal atomic.Int64
	errorsTotal   atomic.Int64
)

// IncRequests incrementa el contador global de requests. Llamar desde middleware.
func IncRequests() { requestsTotal.Add(1) }

// IncErrors incrementa el contador global de errores 5xx. Llamar desde middleware.
func IncErrors() { errorsTotal.Add(1) }

type response struct {
	UptimeSeconds float64 `json:"uptimeSeconds"`
	RequestsTotal int64   `json:"requestsTotal"`
	ErrorsTotal   int64   `json:"errorsTotal"`
	Goroutines    int     `json:"goroutines"`
	MemAllocMB    float64 `json:"memAllocMB"`
	DB            dbStats `json:"db"`
}

type dbStats struct {
	AcquiredConns int32 `json:"acquiredConns"`
	IdleConns     int32 `json:"idleConns"`
	TotalConns    int32 `json:"totalConns"`
	MaxConns      int32 `json:"maxConns"`
}

// Handler devuelve un http.HandlerFunc que responde con métricas JSON.
// Solo debe registrarse en rutas internas (no exponer al público).
func Handler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		st := pool.Stat()
		resp := response{
			UptimeSeconds: time.Since(startTime).Seconds(),
			RequestsTotal: requestsTotal.Load(),
			ErrorsTotal:   errorsTotal.Load(),
			Goroutines:    runtime.NumGoroutine(),
			MemAllocMB:    float64(mem.Alloc) / 1024 / 1024,
			DB: dbStats{
				AcquiredConns: st.AcquiredConns(),
				IdleConns:     st.IdleConns(),
				TotalConns:    st.TotalConns(),
				MaxConns:      st.MaxConns(),
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

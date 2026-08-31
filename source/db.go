package main

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"nettrack/engine"

	_ "modernc.org/sqlite"
)

type AnalyticsPoint struct {
	Timestamp int64   `json:"timestamp"`
	Download  float64 `json:"download"`
	Upload    float64 `json:"upload"`
	Ping      float64 `json:"ping"`
	Jitter    float64 `json:"jitter"`
	Provider  string  `json:"provider"`
	Status    string  `json:"status"`
}

type MetricStats struct {
	Min float64 `json:"min"`
	Avg float64 `json:"avg"`
	Max float64 `json:"max"`
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P95 float64 `json:"p95"`
}

type AnalyticsSummary struct {
	TotalTests      int              `json:"total_tests"`
	SuccessfulTests int              `json:"successful_tests"`
	FailedTests     int              `json:"failed_tests"`
	SuccessRate     float64          `json:"success_rate"`
	TotalBytesDl    int64            `json:"total_bytes_dl"`
	TotalBytesUl    int64            `json:"total_bytes_ul"`
	TotalDataGB     float64          `json:"total_data_gb"`
	Download        MetricStats      `json:"download"`
	Upload          MetricStats      `json:"upload"`
	Ping            MetricStats      `json:"ping"`
	Jitter          MetricStats      `json:"jitter"`
	Points          []AnalyticsPoint `json:"points"`
}

type FilterOptions struct {
	Providers []string           `json:"providers"`
	Servers   []ServerFilterItem `json:"servers"`
}

type ServerFilterItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

type DB struct {
	db *sql.DB
}

func initDB(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("error creating data directory: %w", err)
	}

	dbPath := filepath.Join(dataDir, "nettrack.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("error opening database: %w", err)
	}

	db.SetMaxOpenConns(1)

	schema := `
	CREATE TABLE IF NOT EXISTS results (
		id TEXT PRIMARY KEY,
		timestamp INTEGER NOT NULL,
		provider TEXT NOT NULL,
		server_id TEXT NOT NULL,
		server_name TEXT NOT NULL,
		server_country TEXT NOT NULL,
		server_sponsor TEXT NOT NULL,
		server_host TEXT NOT NULL,
		download_mbps REAL NOT NULL,
		upload_mbps REAL NOT NULL,
		ping_ms REAL NOT NULL,
		jitter_ms REAL NOT NULL,
		duration_s REAL NOT NULL,
		bytes_downloaded INTEGER NOT NULL,
		bytes_uploaded INTEGER NOT NULL,
		is_scheduled INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'success',
		error TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_results_timestamp ON results(timestamp DESC);

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		created_at INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_created ON sessions(created_at);
	`

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("error executing schema: %w", err)
	}

	db.Exec("ALTER TABLE results ADD COLUMN status TEXT NOT NULL DEFAULT 'success'")
	db.Exec("ALTER TABLE results ADD COLUMN error TEXT NOT NULL DEFAULT ''")

	return &DB{db: db}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) SaveResult(r *engine.Result) error {
	query := `
	INSERT INTO results (
		id, timestamp, provider, server_id, server_name, server_country,
		server_sponsor, server_host, download_mbps, upload_mbps, ping_ms,
		jitter_ms, duration_s, bytes_downloaded, bytes_uploaded, is_scheduled,
		status, error, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	isSched := 0
	if r.IsScheduled {
		isSched = 1
	}
	status := r.Status
	if status == "" {
		status = "success"
	}

	_, err := d.db.Exec(
		query,
		r.ID, r.Timestamp, r.Provider, r.ServerID, r.ServerName, r.ServerCountry,
		r.ServerSponsor, r.ServerHost, r.DownloadMbps, r.UploadMbps, r.PingMs,
		r.JitterMs, r.DurationS, r.BytesDownloaded, r.BytesUploaded, isSched,
		status, r.Error, r.CreatedAt,
	)
	return err
}

func (d *DB) ListResults(limit int, offset int) ([]*engine.Result, int, error) {
	var total int
	err := d.db.QueryRow("SELECT COUNT(*) FROM results").Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := d.db.Query(
		`SELECT id, timestamp, provider, server_id, server_name, server_country,
		server_sponsor, server_host, download_mbps, upload_mbps, ping_ms,
		jitter_ms, duration_s, bytes_downloaded, bytes_uploaded, is_scheduled,
		status, error, created_at
		FROM results ORDER BY timestamp DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	results := make([]*engine.Result, 0)
	for rows.Next() {
		var r engine.Result
		var isSched int
		err := rows.Scan(
			&r.ID, &r.Timestamp, &r.Provider, &r.ServerID, &r.ServerName, &r.ServerCountry,
			&r.ServerSponsor, &r.ServerHost, &r.DownloadMbps, &r.UploadMbps, &r.PingMs,
			&r.JitterMs, &r.DurationS, &r.BytesDownloaded, &r.BytesUploaded, &isSched,
			&r.Status, &r.Error, &r.CreatedAt,
		)
		if err != nil {
			return nil, 0, err
		}
		r.IsScheduled = isSched == 1
		results = append(results, &r)
	}

	return results, total, nil
}

func (d *DB) DeleteResult(id string) error {
	_, err := d.db.Exec("DELETE FROM results WHERE id = ?", id)
	return err
}

func (d *DB) PruneResults(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
	res, err := d.db.Exec("DELETE FROM results WHERE timestamp < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (d *DB) GetDistinctFilters() (*FilterOptions, error) {
	pRows, err := d.db.Query("SELECT DISTINCT provider FROM results WHERE provider != '' ORDER BY provider ASC")
	if err != nil {
		return nil, err
	}
	defer pRows.Close()

	providers := make([]string, 0)
	for pRows.Next() {
		var p string
		if err := pRows.Scan(&p); err == nil && p != "" {
			providers = append(providers, p)
		}
	}

	sRows, err := d.db.Query("SELECT DISTINCT server_id, server_name, provider FROM results WHERE server_id != '' ORDER BY server_name ASC")
	if err != nil {
		return nil, err
	}
	defer sRows.Close()

	servers := make([]ServerFilterItem, 0)
	for sRows.Next() {
		var item ServerFilterItem
		if err := sRows.Scan(&item.ID, &item.Name, &item.Provider); err == nil {
			if item.Name == "" {
				item.Name = item.ID
			}
			servers = append(servers, item)
		}
	}

	return &FilterOptions{
		Providers: providers,
		Servers:   servers,
	}, nil
}

func calcStats(vals []float64) MetricStats {
	if len(vals) == 0 {
		return MetricStats{}
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)

	var sum float64
	for _, v := range sorted {
		sum += v
	}
	n := float64(len(sorted))
	avg := math.Round((sum/n)*100) / 100

	calcP := func(p float64) float64 {
		idx := int(math.Floor(p * float64(len(sorted)-1)))
		return math.Round(sorted[idx]*100) / 100
	}

	return MetricStats{
		Min: math.Round(sorted[0]*100) / 100,
		Avg: avg,
		Max: math.Round(sorted[len(sorted)-1]*100) / 100,
		P50: calcP(0.50),
		P90: calcP(0.90),
		P95: calcP(0.95),
	}
}

func (d *DB) GetAnalytics(rangeKey string, provider string, serverID string) (*AnalyticsSummary, error) {
	now := time.Now().Unix()
	var cutoff int64

	switch rangeKey {
	case "24h":
		cutoff = now - 24*3600
	case "7d":
		cutoff = now - 7*24*3600
	case "30d":
		cutoff = now - 30*24*3600
	default:
		cutoff = 0
	}

	query := `
	SELECT id, timestamp, provider, download_mbps, upload_mbps, ping_ms, jitter_ms,
	       bytes_downloaded, bytes_uploaded, status
	FROM results
	WHERE timestamp >= ?
	`
	args := []interface{}{cutoff}

	if provider != "" && provider != "all" {
		query += " AND provider = ?"
		args = append(args, provider)
	}

	if serverID != "" && serverID != "all" {
		query += " AND server_id = ?"
		args = append(args, serverID)
	}

	query += " ORDER BY timestamp ASC"

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := make([]AnalyticsPoint, 0)
	var dlVals, ulVals, pingVals, jitterVals []float64
	var totalBytesDl, totalBytesUl int64
	var successCount, failedCount int

	for rows.Next() {
		var id, prov, status string
		var ts, bDl, bUl int64
		var dl, ul, ping, jitter float64
		if err := rows.Scan(&id, &ts, &prov, &dl, &ul, &ping, &jitter, &bDl, &bUl, &status); err != nil {
			return nil, err
		}

		if status == "failed" {
			failedCount++
		} else {
			status = "success"
			successCount++
			dlVals = append(dlVals, dl)
			ulVals = append(ulVals, ul)
			pingVals = append(pingVals, ping)
			jitterVals = append(jitterVals, jitter)
		}

		totalBytesDl += bDl
		totalBytesUl += bUl

		points = append(points, AnalyticsPoint{
			Timestamp: ts,
			Download:  dl,
			Upload:    ul,
			Ping:      ping,
			Jitter:    jitter,
			Provider:  prov,
			Status:    status,
		})
	}

	totalTests := successCount + failedCount
	var successRate float64
	if totalTests > 0 {
		successRate = math.Round((float64(successCount)/float64(totalTests))*1000) / 10
	}

	totalDataBytes := totalBytesDl + totalBytesUl
	totalDataGB := math.Round((float64(totalDataBytes)/(1024*1024*1024))*100) / 100

	return &AnalyticsSummary{
		TotalTests:      totalTests,
		SuccessfulTests: successCount,
		FailedTests:     failedCount,
		SuccessRate:     successRate,
		TotalBytesDl:    totalBytesDl,
		TotalBytesUl:    totalBytesUl,
		TotalDataGB:     totalDataGB,
		Download:        calcStats(dlVals),
		Upload:          calcStats(ulVals),
		Ping:            calcStats(pingVals),
		Jitter:          calcStats(jitterVals),
		Points:          points,
	}, nil
}

func (d *DB) GetSetting(key string) (string, error) {
	var val string
	err := d.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
	if err != nil {
		return "", err
	}
	return val, nil
}

func (d *DB) SetSetting(key string, val string) error {
	_, err := d.db.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, val)
	return err
}

func (d *DB) GetAllSettings() (map[string]string, error) {
	rows, err := d.db.Query("SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			settings[k] = v
		}
	}
	return settings, nil
}

func (d *DB) CreateSession(token string) error {
	_, err := d.db.Exec("INSERT INTO sessions (token, created_at) VALUES (?, ?)", token, time.Now().Unix())
	return err
}

func (d *DB) DeleteSession(token string) error {
	_, err := d.db.Exec("DELETE FROM sessions WHERE token = ?", token)
	return err
}

func (d *DB) VerifySession(token string) bool {
	var createdAt int64
	err := d.db.QueryRow("SELECT created_at FROM sessions WHERE token = ?", token).Scan(&createdAt)
	if err != nil {
		return false
	}
	if time.Since(time.Unix(createdAt, 0)) > 30*24*time.Hour {
		d.DeleteSession(token)
		return false
	}
	return true
}

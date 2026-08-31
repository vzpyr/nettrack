package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nettrack/engine"
)

//go:embed static/*
var staticFS embed.FS

type API struct {
	config    *Config
	db        *DB
	manager   *engine.Manager
	scheduler *Scheduler
	auth      *AuthHandler
	sse       *SSEHandler
}

func NewAPI(config *Config, db *DB, manager *engine.Manager, scheduler *Scheduler, auth *AuthHandler) *API {
	return &API{
		config:    config,
		db:        db,
		manager:   manager,
		scheduler: scheduler,
		auth:      auth,
		sse:       NewSSEHandler(manager),
	}
}

func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/auth/status", a.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", a.handleLogout)

	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/speedtest/status", a.handleSpeedtestStatus)
	protected.Handle("GET /api/speedtest/events", a.sse)
	protected.HandleFunc("POST /api/speedtest/run", a.handleSpeedtestRun)
	protected.HandleFunc("POST /api/speedtest/cancel", a.handleSpeedtestCancel)
	protected.HandleFunc("GET /api/speedtest/history", a.handleHistoryList)
	protected.HandleFunc("DELETE /api/speedtest/history/{id}", a.handleHistoryDelete)
	protected.HandleFunc("GET /api/speedtest/filters", a.handleSpeedtestFilters)
	protected.HandleFunc("GET /api/speedtest/analytics", a.handleAnalytics)
	protected.HandleFunc("GET /api/servers", a.handleServersList)
	protected.HandleFunc("GET /api/settings", a.handleSettingsGet)
	protected.HandleFunc("POST /api/settings", a.handleSettingsPost)

	mux.Handle("/api/", a.auth.Middleware(protected))

	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("error creating sub filesystem for static: %v", err)
	}
	fileServer := http.FileServer(http.FS(subFS))
	mux.Handle("/", fileServer)

	return mux
}

func (a *API) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func (a *API) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	authed := a.auth.IsAuthenticated(r)
	a.writeJSON(w, http.StatusOK, map[string]bool{"authenticated": authed})
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if !a.auth.Login(w, r, body.Password) {
		a.writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid password"})
		return
	}

	a.writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	a.auth.Logout(w, r)
	a.writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (a *API) handleSpeedtestStatus(w http.ResponseWriter, r *http.Request) {
	status := a.manager.GetStatus()
	a.writeJSON(w, http.StatusOK, status)
}

func (a *API) handleSpeedtestRun(w http.ResponseWriter, r *http.Request) {
	if a.manager.IsRunning() {
		a.writeJSON(w, http.StatusConflict, map[string]string{"error": "a speedtest is already running"})
		return
	}

	var body struct {
		Provider string `json:"provider"`
		ServerID string `json:"server_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Provider = "cloudflare"
	}
	if body.Provider == "" {
		body.Provider = "cloudflare"
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		result, err := a.manager.RunTest(ctx, body.Provider, body.ServerID, false)
		if result != nil {
			if saveErr := a.db.SaveResult(result); saveErr != nil {
				log.Printf("error saving speedtest result: %v", saveErr)
			}
		}
		if err != nil {
			log.Printf("error running speedtest: %v", err)
			return
		}

		retentionStr, err := a.db.GetSetting("retention_days")
		if err == nil && retentionStr != "" {
			if days, err := strconv.Atoi(retentionStr); err == nil && days > 0 {
				a.db.PruneResults(days)
			}
		}
	}()

	a.writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (a *API) handleSpeedtestCancel(w http.ResponseWriter, r *http.Request) {
	a.manager.CancelActive()
	a.writeJSON(w, http.StatusOK, map[string]string{"status": "canceling"})
}

func (a *API) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 100
	offset := 0

	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 500 {
		limit = l
	}
	if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
		offset = o
	}

	results, total, err := a.db.ListResults(limit, offset)
	if err != nil {
		a.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	a.writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

func (a *API) handleHistoryDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		a.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id parameter"})
		return
	}

	if err := a.db.DeleteResult(id); err != nil {
		a.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	a.writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *API) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	rangeKey := r.URL.Query().Get("range")
	if rangeKey == "" {
		rangeKey = "7d"
	}
	provider := r.URL.Query().Get("provider")
	server := r.URL.Query().Get("server")

	analytics, err := a.db.GetAnalytics(rangeKey, provider, server)
	if err != nil {
		a.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	a.writeJSON(w, http.StatusOK, analytics)
}

func (a *API) handleSpeedtestFilters(w http.ResponseWriter, r *http.Request) {
	filters, err := a.db.GetDistinctFilters()
	if err != nil {
		a.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	a.writeJSON(w, http.StatusOK, filters)
}

func (a *API) handleServersList(w http.ResponseWriter, r *http.Request) {
	providerType := r.URL.Query().Get("type")
	if providerType == "" {
		providerType = "cloudflare"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	servers, err := a.manager.ListServers(ctx, providerType)
	if err != nil {
		a.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	a.writeJSON(w, http.StatusOK, servers)
}

func (a *API) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := a.db.GetAllSettings()
	if err != nil {
		a.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	defaults := map[string]string{
		"cron_enabled":    "false",
		"cron_expression": "0 */6 * * *",
		"cron_provider":   "cloudflare",
		"cron_server_id":  "auto",
		"retention_days":  "0",
	}

	for k, v := range defaults {
		if _, ok := settings[k]; !ok {
			settings[k] = v
		}
	}

	a.writeJSON(w, http.StatusOK, settings)
}

func (a *API) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	allowedKeys := []string{"cron_enabled", "cron_expression", "cron_provider", "cron_server_id", "retention_days"}
	for _, k := range allowedKeys {
		if v, ok := body[k]; ok {
			if err := a.db.SetSetting(k, strings.TrimSpace(v)); err != nil {
				a.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
	}

	if err := a.scheduler.Reload(); err != nil {
		a.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid cron configuration: " + err.Error()})
		return
	}

	a.writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

package main

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"nettrack/engine"

	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	mu      sync.Mutex
	cron    *cron.Cron
	entryID cron.EntryID
	db      *DB
	manager *engine.Manager
}

func NewScheduler(db *DB, manager *engine.Manager) *Scheduler {
	parser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)
	return &Scheduler{
		cron:    cron.New(cron.WithParser(parser)),
		db:      db,
		manager: manager,
	}
}

func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cron.Start()
	return s.reloadLocked()
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cron.Stop()
}

func (s *Scheduler) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked()
}

func (s *Scheduler) reloadLocked() error {
	if s.entryID != 0 {
		s.cron.Remove(s.entryID)
		s.entryID = 0
	}

	settings, err := s.db.GetAllSettings()
	if err != nil {
		return err
	}

	enabled := settings["cron_enabled"] == "true"
	expr := settings["cron_expression"]
	if !enabled || expr == "" {
		return nil
	}

	provider := settings["cron_provider"]
	if provider == "" {
		provider = "cloudflare"
	}
	serverID := settings["cron_server_id"]

	entryID, err := s.cron.AddFunc(expr, func() {
		s.executeJob(provider, serverID)
	})
	if err != nil {
		return err
	}

	s.entryID = entryID
	return nil
}

func (s *Scheduler) executeJob(provider string, serverID string) {
	if s.manager.IsRunning() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := s.manager.RunTest(ctx, provider, serverID, true)
	if result != nil {
		if saveErr := s.db.SaveResult(result); saveErr != nil {
			log.Printf("error saving scheduled test result: %v", saveErr)
		}
	}
	if err != nil {
		log.Printf("error executing scheduled test: %v", err)
		return
	}

	retentionStr, err := s.db.GetSetting("retention_days")
	if err == nil && retentionStr != "" {
		if days, err := strconv.Atoi(retentionStr); err == nil && days > 0 {
			s.db.PruneResults(days)
		}
	}
}

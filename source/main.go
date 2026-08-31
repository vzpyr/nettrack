package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nettrack/engine"
)

func main() {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	db, err := initDB(config.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	mgr := engine.NewManager()
	mgr.Register(engine.NewCloudflareProvider())
	mgr.Register(engine.NewLibreSpeedProvider())
	mgr.Register(engine.NewOoklaProvider())

	scheduler := NewScheduler(db, mgr)
	if err := scheduler.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "error starting scheduler: %v\n", err)
		os.Exit(1)
	}
	defer scheduler.Stop()

	auth := NewAuthHandler(config, db)
	api := NewAPI(config, db, mgr, scheduler, auth)

	addr := fmt.Sprintf("0.0.0.0:%d", config.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      api.Routes(),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
}

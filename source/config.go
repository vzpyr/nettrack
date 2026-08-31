package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	Port     int
	Password string
	DataDir  string
}

func loadConfig() (*Config, error) {
	portStr := os.Getenv("NETTRACK_PORT")
	if portStr == "" {
		return nil, fmt.Errorf("error: NETTRACK_PORT environment variable is required")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("error: NETTRACK_PORT must be a valid port number (1-65535): %s", portStr)
	}

	password := os.Getenv("NETTRACK_PASSWORD")
	if password == "" {
		return nil, fmt.Errorf("error: NETTRACK_PASSWORD environment variable is required")
	}

	dataDir := os.Getenv("NETTRACK_DATA_DIR")
	if dataDir == "" {
		return nil, fmt.Errorf("error: NETTRACK_DATA_DIR environment variable is required")
	}
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("error: invalid NETTRACK_DATA_DIR path: %w", err)
	}

	return &Config{
		Port:     port,
		Password: password,
		DataDir:  absDataDir,
	}, nil
}

package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// setupLogging configures date-based log files in ~/.local/state/infrss/logs/.
// Old log files (>7 days) are automatically cleaned up.
func setupLogging() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("Failed to get home directory: %v", err)
		return ""
	}
	logDir := filepath.Join(home, ".local", "state", "infrss", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("Failed to create log directory %s: %v", logDir, err)
		return ""
	}

	dateStr := time.Now().Format("20060102")
	logFile := filepath.Join(logDir, "infrss_"+dateStr+".log")

	// Clean up old log files
	entries, err := os.ReadDir(logDir)
	if err == nil {
		cutoff := time.Now().Add(-7 * 24 * time.Hour)
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "infrss_") && strings.HasSuffix(e.Name(), ".log") {
				info, err := e.Info()
				if err == nil && info.ModTime().Before(cutoff) {
					os.Remove(filepath.Join(logDir, e.Name()))
				}
			}
		}
	}

	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("Failed to open log file %s: %v", logFile, err)
		return ""
	}

	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	return logFile
}

package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	serverassets "github.com/AwesomeDog/infinite-rss-reader/embed"
)

type feedHealth struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	FolderPath  string `json:"folderPath"`
	State       string `json:"state"`
	LastAttempt string `json:"lastAttempt"`
	LastSuccess string `json:"lastSuccess"`
	Failures24h int    `json:"failures24h"`
	Error       string `json:"error"`
}

func registerHealthRoutes(mux *http.ServeMux, feeds []Feed, logDir string) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(serverassets.HealthHTML)
	})
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		items, summary, err := readFeedHealth(feeds, logDir)
		if err != nil {
			log.Print(err)
			sendJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
			return
		}
		sendJSON(w, http.StatusOK, map[string]any{"status": "success", "data": items, "summary": summary})
	})
}

func readFeedHealth(feeds []Feed, logDir string) ([]feedHealth, map[string]int, error) {
	items, byURL := make([]feedHealth, len(feeds)), make(map[string]int, len(feeds))
	cutoff := time.Now().Add(-24 * time.Hour).Format("2006/01/02 15:04:05.000000")
	for i, feed := range feeds {
		title := feed.Title
		if title == "" {
			title = feed.URL
		}
		items[i] = feedHealth{Title: title, URL: feed.URL, FolderPath: feed.FolderPath, State: "pending"}
		byURL[feed.URL] = i
	}

	entries, err := os.ReadDir(logDir)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".log" {
			continue
		}
		file, err := os.Open(filepath.Join(logDir, entry.Name()))
		if err != nil {
			return nil, nil, err
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if len(line) < 26 || !strings.Contains(line, " rss method=GET ") {
				continue
			}
			var url, message string
			var code int
			if n, _ := fmt.Sscanf(line[26:], " rss method=GET url=%q status=%d error=%q", &url, &code, &message); n < 2 {
				continue
			}
			index, ok := byURL[url]
			if !ok {
				continue
			}
			status := &items[index]
			at := line[:26]
			if (code < 200 || code >= 300) && at >= cutoff {
				status.Failures24h++
			}
			if code >= 200 && code < 300 && at > status.LastSuccess {
				status.LastSuccess = at
			}
			if at < status.LastAttempt {
				continue
			}
			status.LastAttempt = at
			if code >= 200 && code < 300 {
				status.State, status.Error = "healthy", ""
			} else {
				if message == "" {
					message = strings.TrimSpace(fmt.Sprintf("HTTP %d %s", code, http.StatusText(code)))
				}
				status.State, status.Error = "error", message
			}
		}
		scanErr := scanner.Err()
		file.Close()
		if scanErr != nil {
			return nil, nil, scanErr
		}
	}

	summary := map[string]int{"total": len(items), "healthy": 0, "error": 0, "pending": 0}
	for _, item := range items {
		summary[item.State]++
	}
	return items, summary, nil
}

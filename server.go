package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

const httpPort = 7654

// runHTTPServer starts the HTTP server on the configured port.
func runHTTPServer(state *BridgeState) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			handleIndex(w, r)
		} else {
			sendJSONError(w, "Not found", http.StatusNotFound)
		}
	})
	mux.HandleFunc("/api/rss/unread", func(w http.ResponseWriter, r *http.Request) {
		handleGetUnread(w, r, state)
	})
	mux.HandleFunc("/api/rss/item", func(w http.ResponseWriter, r *http.Request) {
		handleGetItem(w, r, state)
	})
	mux.HandleFunc("/api/rss/folder", func(w http.ResponseWriter, r *http.Request) {
		handleGetFolder(w, r, state)
	})
	mux.HandleFunc("/api/rss/mark-read", func(w http.ResponseWriter, r *http.Request) {
		handleMarkRead(w, r, state)
	})

	addr := fmt.Sprintf(":%d", httpPort)
	log.Printf("Starting HTTP server on %s", addr)
	if err := http.ListenAndServe(addr, withCORS(mux)); err != nil {
		log.Printf("HTTP server error: %v", err)
	}
}

// withCORS wraps a handler to add CORS headers.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		h.ServeHTTP(w, r)
	})
}

func sendJSON(w http.ResponseWriter, data interface{}, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func sendJSONError(w http.ResponseWriter, message string, status int) {
	sendJSON(w, map[string]interface{}{
		"error":  message,
		"status": "error",
	}, status)
}

// handleIndex serves the embedded index.html.
func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// handleGetUnread requests fresh unread items from the extension.
func handleGetUnread(w http.ResponseWriter, r *http.Request, state *BridgeState) {
	log.Println("API: Requesting unread RSS items")
	state.DrainDataCh()
	sendMessage(map[string]interface{}{"action": "getUnreadRSS"})

	select {
	case <-state.dataCh:
		items := state.GetRSSData()
		log.Printf("API: Returning %d items", len(items))
		sendJSON(w, map[string]interface{}{
			"status": "success",
			"data":   items,
			"count":  len(items),
		}, http.StatusOK)
	case <-time.After(20 * time.Second):
		log.Println("API: Timeout waiting for data update")
		items := state.GetRSSData()
		sendJSON(w, map[string]interface{}{
			"status":  "timeout",
			"data":    items,
			"message": "Timeout waiting for fresh data, returning cached.",
		}, http.StatusOK)
	}
}

// handleGetItem fetches a single item by ID.
func handleGetItem(w http.ResponseWriter, r *http.Request, state *BridgeState) {
	itemID := r.URL.Query().Get("itemId")
	if itemID == "" {
		sendJSONError(w, "itemId is required", http.StatusBadRequest)
		return
	}

	log.Printf("API: Requesting single item %s", itemID)
	ch := state.RegisterSingleItem(itemID)
	sendMessage(map[string]interface{}{
		"action": "getSingleItem",
		"itemId": itemID,
	})

	select {
	case data := <-ch:
		if data != nil {
			sendJSON(w, map[string]interface{}{
				"status": "success",
				"data":   data,
			}, http.StatusOK)
		} else {
			sendJSONError(w, "Item not found", http.StatusNotFound)
		}
	case <-time.After(10 * time.Second):
		log.Printf("API: Timeout getting item %s", itemID)
		state.CleanupSingleItem(itemID)
		sendJSONError(w, "Operation timed out", http.StatusGatewayTimeout)
	}
}

// handleGetFolder fetches all items in a folder path.
func handleGetFolder(w http.ResponseWriter, r *http.Request, state *BridgeState) {
	folderPath := r.URL.Query().Get("folder")
	if folderPath == "" {
		sendJSONError(w, "folder is required", http.StatusBadRequest)
		return
	}

	log.Printf("API: Requesting folder items for %s", folderPath)
	ch := state.RegisterFolder(folderPath)
	sendMessage(map[string]interface{}{
		"action":     "getFolderItems",
		"folderPath": folderPath,
	})

	select {
	case items := <-ch:
		log.Printf("API: Returning %d items for folder %s", len(items), folderPath)
		sendJSON(w, map[string]interface{}{
			"status":     "success",
			"data":       items,
			"count":      len(items),
			"folderPath": folderPath,
		}, http.StatusOK)
	case <-time.After(30 * time.Second):
		log.Printf("API: Timeout getting folder %s", folderPath)
		state.CleanupFolder(folderPath)
		sendJSONError(w, "Operation timed out", http.StatusGatewayTimeout)
	}
}

// handleMarkRead marks a single item as read.
func handleMarkRead(w http.ResponseWriter, r *http.Request, state *BridgeState) {
	itemID := r.URL.Query().Get("itemId")
	if itemID == "" {
		sendJSONError(w, "itemId is required", http.StatusBadRequest)
		return
	}

	log.Printf("API: Marking item %s as read", itemID)
	ch := state.RegisterMarkRead(itemID)
	sendMessage(map[string]interface{}{
		"action": "markAsRead",
		"itemId": itemID,
	})

	select {
	case success := <-ch:
		status := "success"
		msg := fmt.Sprintf("Item %s marked as read", itemID)
		if !success {
			status = "failed"
			msg = fmt.Sprintf("Item %s failed to mark as read", itemID)
		}
		sendJSON(w, map[string]interface{}{
			"status":  status,
			"message": msg,
		}, http.StatusOK)
	case <-time.After(5 * time.Second):
		log.Printf("API: Timeout marking item %s", itemID)
		state.CleanupMarkRead(itemID)
		sendJSONError(w, "Operation timed out", http.StatusGatewayTimeout)
	}
}

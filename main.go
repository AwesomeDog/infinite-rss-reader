package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// version is injected at build time via -ldflags "-X main.version=..."
var version = "dev"

// silentInstall suppresses interactive prompts (waitForEnter) when true.
// Set by --install flag for non-interactive package manager installs (brew, winget).
var silentInstall bool

func main() {
	// --help
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		fmt.Println("Infinite RSS Reader")
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  infrss              Run installer (double-click)")
		fmt.Println("  infrss --install    Run installer non-interactively (brew/winget)")
		fmt.Println("  infrss --version    Print version and exit")
		fmt.Println("  infrss --help       Print this help and exit")
		fmt.Println()
		fmt.Println("When launched by Thunderbird (with extension ID infrss@awesomedog.github.io")
		fmt.Println("as argument), the binary enters bridge mode automatically.")
		fmt.Println()
		fmt.Println("Logs: ~/.local/state/infrss/logs/")
		fmt.Println("Home: https://awesomedog.github.io/infinite-rss-reader/")
		os.Exit(0)
	}

	// --version
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v") {
		fmt.Printf("infrss %s\n", version)
		os.Exit(0)
	}

	// --install (non-interactive, for package managers like brew/winget)
	if len(os.Args) > 1 && os.Args[1] == "--install" {
		silentInstall = true
		runInstallMode()
		return
	}

	// Bridge mode: Thunderbird passes the extension ID as an argument.
	// Match any argument containing "@" (extension ID format) to handle
	// both the current ID and any previously-installed ID.
	for _, arg := range os.Args[1:] {
		if strings.Contains(arg, "@") {
			runBridgeMode()
			return
		}
	}

	// Install mode: no arguments (user double-clicked)
	runInstallMode()
}

// runBridgeMode handles the Thunderbird native messaging bridge.
func runBridgeMode() {
	logFile := setupLogging()
	log.Println("=== Infinite RSS Reader Bridge Started ===")
	log.Printf("Version: %s", version)
	log.Printf("Logging to: %s", logFile)

	// Silently sync XPI version
	go syncXPI()

	state := NewBridgeState()

	// Start HTTP server in background
	go runHTTPServer(state)

	// Main loop: process messages from Thunderbird extension (stdin)
	for {
		msg := getMessage()
		if msg == nil {
			log.Println("Stdin closed, exiting.")
			break
		}

		switch m := msg.(type) {
		case map[string]interface{}:
			msgType, _ := m["type"].(string)

			switch msgType {
			case "rssData":
				data, _ := m["data"].([]interface{})
				state.UpdateRSSData(data)
				log.Printf("Updated RSS data: %d items", len(data))
				sendMessage(map[string]interface{}{"status": "received"})

			case "singleItemData":
				itemID, _ := m["itemId"].(string)
				data := m["data"]
				log.Printf("Received single item data for %s", itemID)
				state.CompleteSingleItem(itemID, data)
				sendMessage(map[string]interface{}{"status": "acknowledged"})

			case "folderData":
				folderPath, _ := m["folderPath"].(string)
				data, _ := m["data"].([]interface{})
				log.Printf("Received folder data for %s: %d items", folderPath, len(data))
				state.CompleteFolder(folderPath, data)
				sendMessage(map[string]interface{}{"status": "acknowledged"})

			case "markReadResult":
				itemID, _ := m["itemId"].(string)
				success, _ := m["success"].(bool)
				log.Printf("Mark read result for %s: %v", itemID, success)
				state.CompleteMarkRead(itemID, success)
				sendMessage(map[string]interface{}{"status": "acknowledged"})
			}

		case string:
			if m == "ping" {
				sendMessage("pong")
			}
		}

		// Small sleep to prevent tight-looping on errors
		time.Sleep(time.Millisecond)
	}
}

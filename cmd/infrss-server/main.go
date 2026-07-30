package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	serverassets "github.com/AwesomeDog/infinite-rss-reader/embed"
	"github.com/mmcdole/gofeed"
	_ "modernc.org/sqlite"
)

const maxFeedSize = 10 << 20

type Feed struct{ URL, FolderPath string }

type outline struct {
	Text     string    `xml:"text,attr"`
	Title    string    `xml:"title,attr"`
	XMLURL   string    `xml:"xmlUrl,attr"`
	Children []outline `xml:"outline"`
}

type apiItem struct {
	ID         int64  `json:"id"`
	Subject    string `json:"subject"`
	Author     string `json:"author"`
	Date       string `json:"date"`
	FolderPath string `json:"folderPath"`
	Body       string `json:"body"`
	Link       string `json:"link"`
}

type apiHandler func(*http.Request) (any, error)

const schemaSQL = `PRAGMA busy_timeout = 5000;
CREATE TABLE IF NOT EXISTS feed_entries (
 id INTEGER PRIMARY KEY AUTOINCREMENT, feed_url TEXT NOT NULL, folder_path TEXT NOT NULL,
 source_id TEXT, entry_key TEXT NOT NULL, content_hash TEXT NOT NULL, title TEXT NOT NULL DEFAULT '',
 author_name TEXT NOT NULL DEFAULT '', permalink TEXT NOT NULL DEFAULT '', content_html TEXT NOT NULL DEFAULT '',
 published_at INTEGER NOT NULL, fetched_at INTEGER NOT NULL, read_at INTEGER, UNIQUE (feed_url, entry_key));
CREATE INDEX IF NOT EXISTS idx_feed_entries_unread ON feed_entries (published_at DESC, id DESC) WHERE read_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_feed_entries_folder ON feed_entries (folder_path, published_at DESC);`

const upsertSQL = `INSERT INTO feed_entries
 (feed_url, folder_path, source_id, entry_key, content_hash, title, author_name, permalink, content_html, published_at, fetched_at)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(feed_url, entry_key) DO UPDATE SET
 fetched_at=excluded.fetched_at,
 folder_path=CASE WHEN feed_entries.content_hash<>excluded.content_hash THEN excluded.folder_path ELSE feed_entries.folder_path END,
 source_id=CASE WHEN feed_entries.content_hash<>excluded.content_hash THEN excluded.source_id ELSE feed_entries.source_id END,
 published_at=CASE WHEN feed_entries.content_hash<>excluded.content_hash THEN excluded.published_at ELSE feed_entries.published_at END,
 content_hash=excluded.content_hash, title=excluded.title, author_name=excluded.author_name,
 permalink=excluded.permalink, content_html=excluded.content_html`

const selectSQL = `SELECT id, title, author_name, published_at, folder_path, content_html, permalink FROM feed_entries`

var refreshMu sync.Mutex

func main() {
	opmlPath := flag.String("opml", "", "Thunderbird OPML file (required)")
	listen := flag.String("listen", "127.0.0.1:7655", "HTTP listen address")
	interval := flag.Duration("refresh", 100*time.Minute, "feed refresh interval")
	flag.Parse()
	if *opmlPath == "" || *interval <= 0 {
		log.Fatal("--opml is required and --refresh must be positive")
	}

	db := must(openDB("./infrss-server.db"))
	defer db.Close()
	feeds := must(loadFeeds(*opmlPath))

	listener := must(net.Listen("tcp", *listen))
	defer listener.Close()
	log.Printf("serving %d feeds on http://%s", len(feeds), *listen)

	client := &http.Client{Timeout: 15 * time.Second}
	go func() {
		refreshFeeds(db, feeds, client)
		for range time.Tick(*interval) {
			refreshFeeds(db, feeds, client)
		}
	}()

	log.Print(http.Serve(listener, routes(db)))
}

func routes(db *sql.DB) http.Handler {
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(serverassets.IndexHTML)
	}
	mux.HandleFunc("GET /{$}", page)
	mux.HandleFunc("GET /index.html", page)

	mux.Handle("GET /api/rss/unread", apiHandler(func(r *http.Request) (any, error) {
		items, err := queryItems(db, "WHERE read_at IS NULL")
		return map[string]any{"status": "success", "data": items, "count": len(items)}, err
	}))
	mux.Handle("GET /api/rss/item", apiHandler(func(r *http.Request) (any, error) {
		id, err := requestID(r)
		if err != nil {
			return nil, err
		}
		items, err := queryItems(db, "WHERE id = ?", id)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("Item not found")
		}
		return map[string]any{"status": "success", "data": items[0]}, nil
	}))
	mux.Handle("GET /api/rss/folder", apiHandler(func(r *http.Request) (any, error) {
		folder := strings.TrimSpace(r.URL.Query().Get("folder"))
		if folder == "" {
			return nil, fmt.Errorf("folder is required")
		}
		if folder != "/" {
			folder = strings.TrimRight(folder, "/")
		}
		pattern := escapeLike(strings.TrimRight(folder, "/")) + "/%"
		items, err := queryItems(db,
			`WHERE folder_path = ? OR folder_path LIKE ? ESCAPE '\'`, folder, pattern)
		return map[string]any{
			"status": "success", "data": items, "count": len(items), "folderPath": folder,
		}, err
	}))
	mux.Handle("GET /api/rss/mark-read", apiHandler(func(r *http.Request) (any, error) {
		id, err := requestID(r)
		if err != nil {
			return nil, err
		}
		result, err := db.Exec(`UPDATE feed_entries SET read_at=? WHERE id=?`, time.Now().Unix(), id)
		if err != nil {
			return nil, err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return nil, fmt.Errorf("Item not found")
		}
		return map[string]any{"status": "success"}, nil
	}))

	return mux
}

func (handler apiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	value, err := handler(r)
	if err != nil {
		log.Print(err)
		sendJSON(w, http.StatusInternalServerError, map[string]any{"status": "error", "error": err.Error()})
		return
	}
	sendJSON(w, http.StatusOK, value)
}

func must[T any](value T, err error) T {
	if err != nil {
		log.Fatal(err)
	}
	return value
}

func openDB(filename string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func queryItems(db *sql.DB, where string, args ...any) ([]apiItem, error) {
	rows, err := db.Query(selectSQL+" "+where+" ORDER BY published_at DESC, id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]apiItem, 0)
	for rows.Next() {
		var item apiItem
		var published int64
		if err := rows.Scan(&item.ID, &item.Subject, &item.Author, &published,
			&item.FolderPath, &item.Body, &item.Link); err != nil {
			return nil, err
		}
		item.Date = time.Unix(published, 0).UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	return items, rows.Err()
}

func requestID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.URL.Query().Get("itemId"), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("itemId must be a positive integer")
	}
	return id, nil
}

func sendJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func loadFeeds(filename string) ([]Feed, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var document struct {
		XMLName xml.Name  `xml:"opml"`
		Items   []outline `xml:"body>outline"`
	}
	if err := xml.Unmarshal(data, &document); err != nil {
		return nil, err
	}

	feeds := make([]Feed, 0)
	seen := make(map[string]bool)
	var walk func([]outline, string)
	walk = func(nodes []outline, parent string) {
		for _, node := range nodes {
			name := strings.TrimSpace(node.Title)
			if name == "" {
				name = strings.TrimSpace(node.Text)
			}
			name = strings.Trim(name, "/")
			feedURL := strings.TrimSpace(node.XMLURL)
			path := parent
			if feedURL == "" && name != "" {
				path += "/" + name
			}
			if feedURL != "" {
				if seen[feedURL] {
					log.Printf("duplicate feed URL %q ignored", feedURL)
				} else {
					seen[feedURL] = true
					folder := "/" + strings.TrimPrefix(path, "/")
					feeds = append(feeds, Feed{feedURL, folder})
				}
			}
			walk(node.Children, path)
		}
	}
	walk(document.Items, "")
	return feeds, nil
}

func refreshFeeds(db *sql.DB, feeds []Feed, client *http.Client) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	for _, feed := range feeds {
		parsed, err := downloadFeed(client, feed.URL)
		if err != nil {
			log.Printf("refresh %s: %v", feed.URL, err)
			continue
		}
		now := time.Now().UTC()
		for _, item := range parsed.Items {
			if item == nil {
				continue
			}
			if err := storeItem(db, feed, item, now); err != nil {
				log.Printf("store %s: %v", feed.URL, err)
				break
			}
		}
	}
}

func downloadFeed(client *http.Client, feedURL string) (*gofeed.Feed, error) {
	response, err := client.Get(feedURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFeedSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFeedSize {
		return nil, fmt.Errorf("feed exceeds 10 MiB")
	}
	return gofeed.NewParser().Parse(bytes.NewReader(data))
}

func storeItem(db *sql.DB, feed Feed, item *gofeed.Item, now time.Time) error {
	title, link := strings.TrimSpace(item.Title), strings.TrimSpace(item.Link)
	author := ""
	if item.Author != nil {
		author = strings.TrimSpace(item.Author.Name)
	} else if len(item.Authors) > 0 && item.Authors[0] != nil {
		author = strings.TrimSpace(item.Authors[0].Name)
	}
	body := item.Content
	if strings.TrimSpace(body) == "" {
		body = item.Description
	}
	published := now.Unix()
	if item.PublishedParsed != nil {
		published = item.PublishedParsed.Unix()
	} else if item.UpdatedParsed != nil {
		published = item.UpdatedParsed.Unix()
	}

	sourceID := strings.TrimSpace(item.GUID)
	key := "fallback:" + digest(title+strconv.FormatInt(published, 10))
	if sourceID != "" {
		key = "id:" + sourceID
	} else if link != "" {
		key = "link:" + normalizeLink(link)
	}
	hash := digest(strings.Join([]string{title, author, link, body}, "\x00"))
	_, err := db.Exec(upsertSQL, feed.URL, feed.FolderPath,
		sql.NullString{String: sourceID, Valid: sourceID != ""}, key, hash,
		title, author, link, body, published, now.Unix())
	return err
}

func normalizeLink(link string) string {
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return link
	}
	parsed.Fragment = ""
	parsed.Scheme, parsed.Host = strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host)
	return parsed.String()
}

func digest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

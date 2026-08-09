package main

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	serverassets "github.com/AwesomeDog/infinite-rss-reader/embed"
	"github.com/mmcdole/gofeed"
	_ "modernc.org/sqlite"
)

const maxFeedSize, feedRequestTimeout = 10 << 20, 30 * time.Second

var version = "dev"
var feedOrder = make(map[string]int)

type Feed struct{ URL, FolderPath, Title string }

type outline struct {
	Text     string    `xml:"text,attr"`
	Title    string    `xml:"title,attr"`
	XMLURL   string    `xml:"xmlUrl,attr"`
	Children []outline `xml:"outline"`
}

type apiItem struct {
	ID         int64  `json:"id"`
	FeedURL    string `json:"-"`
	Subject    string `json:"subject"`
	Author     string `json:"author"`
	Date       string `json:"date"`
	FolderPath string `json:"folderPath"`
	Body       string `json:"body"`
	Link       string `json:"link"`
}

type apiHandler func(*http.Request) (any, error)

type statusWriter struct {
	http.ResponseWriter
	status int
}

const schemaSQL = `PRAGMA busy_timeout = 5000;
CREATE TABLE IF NOT EXISTS feed_entries (id INTEGER PRIMARY KEY AUTOINCREMENT, feed_url TEXT NOT NULL, folder_path TEXT NOT NULL,
 source_id TEXT, entry_key TEXT NOT NULL, content_hash TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', author_name TEXT NOT NULL DEFAULT '',
 permalink TEXT NOT NULL DEFAULT '', content_html TEXT NOT NULL DEFAULT '', published_at INTEGER NOT NULL, fetched_at INTEGER NOT NULL,
 read_at INTEGER, UNIQUE (feed_url, entry_key));
CREATE INDEX IF NOT EXISTS idx_feed_entries_unread ON feed_entries (published_at DESC, id DESC) WHERE read_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_feed_entries_folder ON feed_entries (folder_path, published_at DESC);`

const upsertSQL = `INSERT INTO feed_entries (feed_url, folder_path, source_id, entry_key, content_hash, title, author_name, permalink,
 content_html, published_at, fetched_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(feed_url, entry_key) DO UPDATE SET
 fetched_at=excluded.fetched_at, folder_path=CASE WHEN feed_entries.content_hash<>excluded.content_hash THEN excluded.folder_path ELSE feed_entries.folder_path END,
 source_id=CASE WHEN feed_entries.content_hash<>excluded.content_hash THEN excluded.source_id ELSE feed_entries.source_id END,
 published_at=CASE WHEN feed_entries.content_hash<>excluded.content_hash THEN excluded.published_at ELSE feed_entries.published_at END,
 content_hash=excluded.content_hash, title=excluded.title, author_name=excluded.author_name, permalink=excluded.permalink, content_html=excluded.content_html`

func main() {
	opmlPath := flag.String("opml", "", "Thunderbird OPML file (required)")
	listen := flag.String("listen", "127.0.0.1:7655", "HTTP listen address")
	interval := flag.Duration("refresh", 100*time.Minute, "feed refresh interval")
	batchSize := flag.Int("batch-size", 10, "feeds per fetch batch")
	batchGap := flag.Duration("batch-gap", 10*time.Second, "delay between fetch batches")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("infrss-server %s\n", version)
		return
	}
	if *opmlPath == "" || *interval <= 0 || *batchSize <= 0 || *batchGap < 0 {
		log.Fatal("--opml is required; --refresh and --batch-size must be positive; --batch-gap must be non-negative")
	}

	stateDir := must(serverStateDir())
	fmt.Printf("using state directory: %s\n", stateDir)
	logFile := must(setupLogging(stateDir))
	defer logFile.Close()

	db := must(openDB(filepath.Join(stateDir, "infrss-server.db")))
	defer db.Close()
	feeds := must(loadFeeds(*opmlPath))

	listener := must(net.Listen("tcp", *listen))
	defer listener.Close()
	fmt.Printf("serving %d feeds on http://%s\n", len(feeds), *listen)
	log.Printf("serving %d feeds on http://%s", len(feeds), *listen)

	go func(client *http.Client) {
		refreshFeeds(db, feeds, client, *batchSize, *batchGap)
		for range time.Tick(*interval) {
			refreshFeeds(db, feeds, client, *batchSize, *batchGap)
		}
	}(&http.Client{Timeout: feedRequestTimeout})

	log.Print(http.Serve(listener, logRequests(routes(db, feeds, filepath.Join(stateDir, "logs")))))
}

func routes(db *sql.DB, feeds []Feed, logDir string) http.Handler {
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(serverassets.IndexHTML)
	}
	mux.HandleFunc("GET /{$}", page)
	mux.HandleFunc("GET /index.html", page)
	registerHealthRoutes(mux, feeds, logDir)

	mux.Handle("GET /api/rss/unread", apiHandler(func(r *http.Request) (any, error) {
		items, err := queryItems(db, "WHERE read_at IS NULL")
		rank := func(item apiItem) int { return cmp.Or(feedOrder[item.FeedURL], len(feedOrder)+1) }
		slices.SortStableFunc(items, func(a, b apiItem) int { return cmp.Compare(rank(a), rank(b)) })
		return map[string]any{"status": "success", "data": items, "count": len(items)}, err
	}))
	mux.Handle("GET /api/rss/item", withItemID(func(id int64) (any, error) {
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
		items, err := queryItems(db, `WHERE folder_path = ? OR folder_path LIKE ? ESCAPE '\'`, folder,
			strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimRight(folder, "/"))+"/%")
		return map[string]any{"status": "success", "data": items, "count": len(items), "folderPath": folder}, err
	}))
	mux.Handle("GET /api/rss/mark-read", withItemID(func(id int64) (any, error) {
		result, err := db.Exec(`UPDATE feed_entries SET read_at=? WHERE id=?`, time.Now().Unix(), id)
		if err != nil {
			return nil, err
		}
		if count, _ := result.RowsAffected(); count == 0 {
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

func withItemID(handler func(int64) (any, error)) apiHandler {
	return func(r *http.Request) (any, error) {
		id, err := strconv.ParseInt(r.URL.Query().Get("itemId"), 10, 64)
		if err != nil || id < 1 {
			return nil, fmt.Errorf("itemId must be a positive integer")
		}
		return handler(id)
	}
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writer := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(writer, r)
		log.Printf("user method=%s uri=%q status=%d", r.Method, r.URL.RequestURI(), cmp.Or(writer.status, http.StatusOK))
	})
}

func must[T any](value T, err error) T {
	if err != nil {
		if log.Writer() != os.Stderr {
			fmt.Fprintln(os.Stderr, err)
		}
		log.Fatal(err)
	}
	return value
}

func serverStateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "infrss-server")
	return dir, os.MkdirAll(dir, 0700)
}

func setupLogging(stateDir string) (*os.File, error) {
	logDir := filepath.Join(stateDir, "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("infrss-server-%s-p%d.log", time.Now().Format("20060102T150405.000000000-0700"), os.Getpid())
	file, err := os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	log.SetOutput(file)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	return file, nil
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
	rows, err := db.Query(`SELECT id, feed_url, title, author_name, published_at, folder_path, content_html, permalink FROM feed_entries `+
		where+" ORDER BY published_at DESC, id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]apiItem, 0)
	for rows.Next() {
		var item apiItem
		var published int64
		if err := rows.Scan(&item.ID, &item.FeedURL, &item.Subject, &item.Author, &published, &item.FolderPath, &item.Body, &item.Link); err != nil {
			return nil, err
		}
		item.Date = time.Unix(published, 0).UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	return items, rows.Err()
}

func sendJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
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

	feeds, seen := make([]Feed, 0), make(map[string]bool)
	var walk func([]outline, string)
	walk = func(nodes []outline, parent string) {
		for _, node := range nodes {
			name, feedURL, path := strings.Trim(cmp.Or(strings.TrimSpace(node.Title), strings.TrimSpace(node.Text)), "/"), strings.TrimSpace(node.XMLURL), parent
			if feedURL == "" {
				if name != "" {
					path += "/" + name
				}
			} else if seen[feedURL] {
				log.Printf("duplicate feed URL %q ignored", feedURL)
			} else {
				seen[feedURL] = true
				feedOrder[feedURL] = len(feeds) + 1
				feeds = append(feeds, Feed{URL: feedURL, FolderPath: "/" + strings.TrimPrefix(path, "/"), Title: name})
			}
			walk(node.Children, path)
		}
	}
	walk(document.Items, "")
	return feeds, nil
}

func refreshFeeds(db *sql.DB, feeds []Feed, client *http.Client, batchSize int, batchGap time.Duration) {
	queue := append([]Feed(nil), feeds...)
	rand.Shuffle(len(queue), func(i, j int) { queue[i], queue[j] = queue[j], queue[i] })

	for start := 0; start < len(queue); start += batchSize {
		end := min(start+batchSize, len(queue))
		batch := queue[start:end]
		parsed, errs := make([]*gofeed.Feed, len(batch)), make([]error, len(batch))

		var batchWG sync.WaitGroup
		batchWG.Add(len(batch))
		for i, feed := range batch {
			go func(i int, feed Feed) {
				defer batchWG.Done()
				parsed[i], errs[i] = downloadFeed(client, feed.URL)
			}(i, feed)
		}
		batchWG.Wait()

		for i, feed := range batch {
			if errs[i] != nil {
				log.Printf("rss err url=%q error=%q", feed.URL, errs[i])
				continue
			}
			now := time.Now().UTC()
			for _, item := range parsed[i].Items {
				if item == nil {
					continue
				}
				if err := storeItem(db, feed, item, now); err != nil {
					log.Printf("store err %s: %v", feed.URL, err)
					break
				}
			}
		}

		if end < len(queue) {
			time.Sleep(batchGap)
		}
	}
}

func downloadFeed(client *http.Client, feedURL string) (*gofeed.Feed, error) {
	response, err := client.Get(feedURL)
	if err != nil {
		log.Printf("rss method=GET url=%q status=0 error=%q", feedURL, err)
		return nil, err
	}
	defer response.Body.Close()
	log.Printf("rss method=GET url=%q status=%d", feedURL, response.StatusCode)
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
	if date := cmp.Or(item.PublishedParsed, item.UpdatedParsed); date != nil {
		published = date.Unix()
	}

	sourceID := strings.TrimSpace(item.GUID)
	key := "fallback:" + digest(title+strconv.FormatInt(published, 10))
	if sourceID != "" {
		key = "id:" + sourceID
	} else if link != "" {
		key = "link:" + normalizeLink(link)
	}
	hash := digest(strings.Join([]string{title, author, link, body}, "\x00"))
	var duplicate bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM feed_entries WHERE feed_url=? AND entry_key=? AND content_hash=?)`,
		feed.URL, key, hash).Scan(&duplicate)
	if err != nil || duplicate {
		return err
	}
	_, err = db.Exec(upsertSQL, feed.URL, feed.FolderPath, sql.NullString{String: sourceID, Valid: sourceID != ""}, key,
		hash, title, author, link, body, published, now.Unix())
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

func digest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

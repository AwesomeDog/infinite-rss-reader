# infrss-server

## Goal

`infrss-server` is a standalone RSS reader backend. It reads a Thunderbird OPML file, fetches feeds, stores items in SQLite, and serves the existing `embed/index.html` UI. It does not use Thunderbird, Native Messaging, the extension, or the original bridge.

The backend stays in one `main` package. It has no service layer, repository layer, interfaces, dependency injection, or migration framework.

## Build and run

Go 1.26 or later is required.

```bash
go build -o bin/infrss-server ./cmd/infrss-server

./bin/infrss-server --opml /home/alice/feeds.opml
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--opml` | none | Thunderbird OPML file; required |
| `--listen` | `127.0.0.1:7655` | HTTP listen address |
| `--refresh` | `100m` | Feed refresh interval; must be positive |
| `--version` | `false` | Print the build version and exit |

Runtime state is stored in `$XDG_STATE_HOME/infrss-server`, defaulting to `~/.local/state/infrss-server`. The database is `infrss-server.db` in that directory, and the actual directory is printed at startup. The OPML file is read once at startup; restart the server after changing it.

## Files

| File | Purpose |
| --- | --- |
| `cmd/infrss-server/main.go` | CLI, OPML parsing, SQLite, feed refresh, and HTTP API |
| `embed/server_assets.go` | Embeds the existing `embed/index.html` |
| `go.mod`, `go.sum` | Add `gofeed` and the pure Go SQLite driver |

The original Thunderbird backend is unchanged. Both binaries embed the same physical HTML file.

## Startup

The server starts in this order:

1. Parse and validate flags.
2. Create the state and log directories and open a new log file.
3. Open `infrss-server.db` in the state directory and create the schema.
4. Parse the OPML file.
5. Bind the HTTP listener.
6. Start the initial feed refresh in the background.
7. Serve HTTP while later refreshes run on the configured interval.

The listener is bound before any feed is downloaded. A slow feed or an HTTP error such as `503 Service Unavailable` does not delay port `7655` or the reader UI.

Startup errors are logged unchanged and stop the process. API errors pass through one standard-library handler boundary and return HTTP 500 with the original error text:

```json
{"status":"error","error":"original error text"}
```

There are no error classes or error-code abstractions.

Each start creates a timestamped log file under `$XDG_STATE_HOME/infrss-server/logs`. Log files are not rotated, truncated, or automatically removed. Each feed request and user HTTP request records its URL and response status with a timestamp; a feed request that receives no HTTP response uses status `0`.

## OPML

Only Thunderbird-style OPML is supported. The parser recursively reads `outline` elements and uses only `title`, `text`, `xmlUrl`, and child outlines.

- An outline with `xmlUrl` is a feed.
- Only non-feed outline names are added to `folder_path`.
- The feed outline name itself is not added again.
- `title` is preferred over `text`.
- A feed directly under `body` uses `/`.
- For duplicate `xmlUrl` values, the first feed wins and a warning is logged.

For the structure in `docs/sample.opml`, the Hugging Face feed is stored under:

```text
/Tech/Hugging Face - Blog
```

It is not stored as `/Tech/Hugging Face - Blog/Hugging Face - Blog`.

## Feed refresh

Feeds are shuffled before each refresh and fetched in batches of up to 10 with one shared HTTP client. Requests in a batch run concurrently; the next batch starts 10 seconds after the whole batch finishes. Each request has a 30-second timeout, must return a 2xx status, and may contain at most 10 MiB. `gofeed` parses RSS and Atom.

The first refresh starts immediately after the listener is bound. Later refreshes use `--refresh`. A mutex prevents overlapping refreshes.

A failed feed is logged and skipped without stopping the server or other feeds. A database error while storing an item stops the rest of that feed and continues with the next feed. Existing rows are never deleted because a refresh fails.

The server downloads complete feeds on every refresh. It does not store ETag or Last-Modified values.

## Item data

Content is selected in this order:

1. Feed `Content`.
2. Feed `Description` when `Content` is empty.
3. An empty string when both are missing.

The server does not fetch article pages to fill missing content.

Publication time uses `Published`, then `Updated`, then the fetch time. SQLite stores Unix seconds. The API returns UTC RFC 3339 strings.

The entry key is selected in this order:

```text
guid or Atom id -> "id:" + source_id
link            -> "link:" + normalized_link
otherwise       -> "fallback:" + SHA256(title + published_at)
```

Link normalization removes the fragment and lowercases the scheme and host. `content_hash` is SHA-256 over the title, author, link, and body.

`(feed_url, entry_key)` is unique. A refresh updates the existing row instead of creating a duplicate. `id` and `read_at` are preserved. `fetched_at` is always updated; changed content also updates its stored metadata.

## SQLite

The database contains one `feed_entries` table. It stores the feed URL, folder path, source ID, entry key, content hash, item fields, timestamps, and read state.

`read_at IS NULL` means unread. A non-null value is the Unix time when the item was marked read. Indexes cover unread items and folder queries. SQLite uses a 5-second busy timeout and one open connection.

The OPML file controls only which feeds are fetched during the current process. Removing a feed from OPML does not delete its stored items. The server does not reconcile subscriptions, migrate old schemas, or rewrite legacy rows. A schema change requires a fresh `infrss-server.db` in the state directory.

## HTTP API

| Endpoint | Result |
| --- | --- |
| `GET /` | Reader UI |
| `GET /index.html` | Reader UI |
| `GET /api/rss/unread` | All unread items |
| `GET /api/rss/item?itemId=123` | One item |
| `GET /api/rss/folder?folder=/Tech` | Items in the folder and real child folders |
| `GET /api/rss/mark-read?itemId=123` | Set `read_at` |

An item has this shape:

```json
{
  "id": 123,
  "subject": "Example title",
  "author": "Example author",
  "date": "2026-07-29T08:00:00Z",
  "folderPath": "/Tech/Hacker News",
  "body": "<p>Feed content</p>",
  "link": "https://example.com/item"
}
```

Unread items follow OPML feed order, with `published_at DESC, id DESC` inside each feed; items absent from the current OPML come last. Other lists remain sorted by `published_at DESC, id DESC`. Folder matching uses the exact path or `folder/%`, so `/Tech` does not match `/Technology`. The backend does not paginate because the UI slices items in the browser.

`mark-read` remains a GET endpoint to preserve compatibility with the existing HTML.

## Not included

- Thunderbird or extension integration
- Subscription CRUD or OPML upload
- Article-page content extraction
- ETag or Last-Modified caching
- Pagination or full-text search
- WebSocket, SSE, or automatic UI refresh
- Authentication or LAN exposure
- History cleanup, schema migration, or legacy-data repair
- Tests or test fixtures

#!/usr/bin/env python3

# ============ Configuration ============
HTTP_PORT = 7654
# =======================================

import sys
import json
import struct
import threading
import os
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse, parse_qs
import logging
import tempfile
from pathlib import Path
import time


# --- Logging Setup ---
def setup_logging():
    """Configures logging to a file in the system's temp directory."""
    if sys.platform == 'win32':
        log_dir = Path(os.environ.get('TEMP', tempfile.gettempdir()))
    else:
        log_dir = Path('/tmp')

    # Use date-based log file naming
    from datetime import datetime
    date_str = datetime.now().strftime('%Y%m%d')
    log_file = log_dir / f'thunderbird_rss_bridge_{date_str}.log'

    # Clean up old log files (keep last 7 days)
    try:
        for old_log in log_dir.glob('thunderbird_rss_bridge_*.log'):
            # Check if file is older than 7 days
            if (time.time() - old_log.stat().st_mtime) > (7 * 24 * 3600):
                old_log.unlink()
                logging.info(f"Deleted old log file: {old_log}")
    except Exception as e:
        pass  # Silently ignore cleanup errors

    logging.basicConfig(
        filename=str(log_file),
        level=logging.INFO,  # Changed to INFO to reduce noise
        format='%(asctime)s - %(levelname)s - [%(threadName)s] - %(message)s'
    )
    return log_file


# --- Native Messaging Helpers ---

def get_message():
    """Reads a length-prefixed message from stdin."""
    try:
        raw_length = sys.stdin.buffer.read(4)
        if len(raw_length) == 0:
            return None
        message_length = struct.unpack('@I', raw_length)[0]
        message = sys.stdin.buffer.read(message_length).decode('utf-8')
        return json.loads(message)
    except Exception as e:
        logging.error(f"Error reading message: {e}")
        return None


def send_message(message_content):
    """Sends a length-prefixed message to stdout."""
    try:
        encoded_content = json.dumps(message_content, separators=(',', ':')).encode('utf-8')
        encoded_length = struct.pack('@I', len(encoded_content))
        sys.stdout.buffer.write(encoded_length)
        sys.stdout.buffer.write(encoded_content)
        sys.stdout.buffer.flush()
    except Exception as e:
        logging.error(f"Error sending message: {e}")


# --- State Management ---

class BridgeState:
    """Manages the application state and synchronization."""

    def __init__(self):
        self.latest_rss_data = {"unread_items": [], "timestamp": 0}
        self.data_update_event = threading.Event()
        self.mark_read_results = {}  # Map itemId -> {"event": Event, "success": bool}
        self.single_item_results = {}  # Map itemId -> {"event": Event, "data": item}
        self.folder_results = {}  # Map folderPath -> {"event": Event, "data": items}
        self.lock = threading.Lock()

    def update_rss_data(self, data):
        with self.lock:
            self.latest_rss_data["unread_items"] = data
            self.latest_rss_data["timestamp"] = time.time()
        self.data_update_event.set()

    def get_rss_data(self):
        with self.lock:
            return self.latest_rss_data.get("unread_items", [])

    def register_mark_read_request(self, item_id):
        event = threading.Event()
        with self.lock:
            self.mark_read_results[item_id] = {"event": event, "success": False}
        return event

    def complete_mark_read_request(self, item_id, success):
        with self.lock:
            if item_id in self.mark_read_results:
                self.mark_read_results[item_id]["success"] = success
                self.mark_read_results[item_id]["event"].set()

    def get_mark_read_result(self, item_id):
        with self.lock:
            if item_id in self.mark_read_results:
                result = self.mark_read_results[item_id]["success"]
                del self.mark_read_results[item_id]
                return result
        return False

    def cleanup_mark_read_request(self, item_id):
        with self.lock:
            if item_id in self.mark_read_results:
                del self.mark_read_results[item_id]

    # Single item methods
    def register_single_item_request(self, item_id):
        event = threading.Event()
        with self.lock:
            self.single_item_results[item_id] = {"event": event, "data": None}
        return event

    def complete_single_item_request(self, item_id, data):
        with self.lock:
            if item_id in self.single_item_results:
                self.single_item_results[item_id]["data"] = data
                self.single_item_results[item_id]["event"].set()

    def get_single_item_result(self, item_id):
        with self.lock:
            if item_id in self.single_item_results:
                result = self.single_item_results[item_id]["data"]
                del self.single_item_results[item_id]
                return result
        return None

    def cleanup_single_item_request(self, item_id):
        with self.lock:
            if item_id in self.single_item_results:
                del self.single_item_results[item_id]

    # Folder methods
    def register_folder_request(self, folder_path):
        event = threading.Event()
        with self.lock:
            self.folder_results[folder_path] = {"event": event, "data": []}
        return event

    def complete_folder_request(self, folder_path, data):
        with self.lock:
            if folder_path in self.folder_results:
                self.folder_results[folder_path]["data"] = data
                self.folder_results[folder_path]["event"].set()

    def get_folder_result(self, folder_path):
        with self.lock:
            if folder_path in self.folder_results:
                result = self.folder_results[folder_path]["data"]
                del self.folder_results[folder_path]
                return result
        return []

    def cleanup_folder_request(self, folder_path):
        with self.lock:
            if folder_path in self.folder_results:
                del self.folder_results[folder_path]


state = BridgeState()


# --- HTTP Request Handler ---

class RSSHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        # Override to use our logging configuration
        logging.info("%s - - [%s] %s" % (
            self.address_string(),
            self.log_date_time_string(),
            format % args))

    def _send_json(self, data, status=200):
        self.send_response(status)
        self.send_header('Content-type', 'application/json')
        self.send_header('Access-Control-Allow-Origin', '*')  # Consider restricting this
        self.end_headers()
        self.wfile.write(json.dumps(data).encode('utf-8'))

    def _send_error(self, message, status=500):
        self._send_json({"error": message, "status": "error"}, status)

    def do_GET(self):
        parsed_path = urlparse(self.path)
        path = parsed_path.path
        query_params = parse_qs(parsed_path.query)

        try:
            if path == '/' or path == '/index.html':
                self.handle_index()
            elif path == '/api/rss/unread':
                self.handle_get_unread()
            elif path == '/api/rss/item':
                self.handle_get_item(query_params)
            elif path == '/api/rss/folder':
                self.handle_get_folder(query_params)
            elif path == '/api/rss/mark-read':
                self.handle_mark_read(query_params)
            else:
                self._send_error("Not found", 404)
        except Exception as e:
            logging.error(f"Request error: {e}", exc_info=True)
            self._send_error(str(e))

    def handle_index(self):
        try:
            script_dir = Path(__file__).parent.absolute()
            html_path = script_dir / 'index.html'

            if not html_path.exists():
                self._send_error("index.html not found", 404)
                return

            with open(html_path, 'r', encoding='utf-8') as f:
                html_content = f.read()

            self.send_response(200)
            self.send_header('Content-type', 'text/html; charset=utf-8')
            self.end_headers()
            self.wfile.write(html_content.encode('utf-8'))
        except Exception as e:
            logging.error(f"Error serving index.html: {e}")
            self._send_error("Internal Server Error")

    def handle_get_unread(self):
        logging.info("API: Requesting unread RSS items")
        state.data_update_event.clear()
        send_message({"action": "getUnreadRSS"})

        # Wait for data update (max 20 seconds)
        if state.data_update_event.wait(timeout=20.0):
            items = state.get_rss_data()
            logging.info(f"API: Returning {len(items)} items")
            self._send_json({
                "status": "success",
                "data": items,
                "count": len(items)
            })
        else:
            logging.warning("API: Timeout waiting for data update")
            # Return cached data even on timeout? Or empty? 
            # Returning current state is safer than error.
            items = state.get_rss_data()
            self._send_json({
                "status": "timeout",
                "data": items,
                "message": "Timeout waiting for fresh data, returning cached."
            })

    def handle_mark_read(self, query_params):
        item_id = query_params.get('itemId', [None])[0]
        if not item_id:
            self._send_error("itemId is required", 400)
            return

        logging.info(f"API: Marking item {item_id} as read")

        result_event = state.register_mark_read_request(item_id)
        send_message({
            "action": "markAsRead",
            "itemId": item_id
        })

        if result_event.wait(timeout=5.0):  # Increased timeout slightly
            success = state.get_mark_read_result(item_id)
            self._send_json({
                "status": "success" if success else "failed",
                "message": f"Item {item_id} {'marked as read' if success else 'failed to mark as read'}"
            })
        else:
            logging.warning(f"API: Timeout marking item {item_id}")
            state.cleanup_mark_read_request(item_id)
            self._send_error("Operation timed out", 504)

    def handle_get_item(self, query_params):
        item_id = query_params.get('itemId', [None])[0]
        if not item_id:
            self._send_error("itemId is required", 400)
            return

        logging.info(f"API: Requesting single item {item_id}")

        result_event = state.register_single_item_request(item_id)
        send_message({
            "action": "getSingleItem",
            "itemId": item_id
        })

        if result_event.wait(timeout=10.0):
            item = state.get_single_item_result(item_id)
            if item:
                self._send_json({
                    "status": "success",
                    "data": item
                })
            else:
                self._send_error("Item not found", 404)
        else:
            logging.warning(f"API: Timeout getting item {item_id}")
            state.cleanup_single_item_request(item_id)
            self._send_error("Operation timed out", 504)

    def handle_get_folder(self, query_params):
        folder_path = query_params.get('folder', [None])[0]
        if not folder_path:
            self._send_error("folder is required", 400)
            return

        logging.info(f"API: Requesting folder items for {folder_path}")

        result_event = state.register_folder_request(folder_path)
        send_message({
            "action": "getFolderItems",
            "folderPath": folder_path
        })

        if result_event.wait(timeout=30.0):  # Longer timeout for folder operations
            items = state.get_folder_result(folder_path)
            logging.info(f"API: Returning {len(items)} items for folder {folder_path}")
            self._send_json({
                "status": "success",
                "data": items,
                "count": len(items),
                "folderPath": folder_path
            })
        else:
            logging.warning(f"API: Timeout getting folder {folder_path}")
            state.cleanup_folder_request(folder_path)
            self._send_error("Operation timed out", 504)


def run_http_server(port):
    server_address = ('', port)
    httpd = HTTPServer(server_address, RSSHandler)
    logging.info(f'Starting HTTP server on {server_address[0]}:{port}...')
    try:
        httpd.serve_forever()
    except Exception as e:
        logging.error(f"HTTP Server crashed: {e}")


# --- Main Loop ---

def main():
    log_file = setup_logging()
    logging.info("=== RSS Bridge Started ===")
    logging.info(f"Logging to: {log_file}")

    # Start HTTP server in background thread
    http_thread = threading.Thread(target=run_http_server, args=(HTTP_PORT,), daemon=True, name="HTTP_Thread")
    http_thread.start()

    # Main loop: Process messages from Thunderbird Extension (stdin)
    while True:
        try:
            received_message = get_message()
            if received_message is None:
                logging.info("Stdin closed, exiting.")
                break

            # logging.debug(f"Received: {received_message}") 

            if isinstance(received_message, dict):
                msg_type = received_message.get("type")

                if msg_type == "rssData":
                    data = received_message.get("data", [])
                    state.update_rss_data(data)
                    logging.info(f"Updated RSS data: {len(data)} items")
                    send_message({"status": "received"})

                elif msg_type == "singleItemData":
                    item_id = received_message.get("itemId")
                    data = received_message.get("data")
                    logging.info(f"Received single item data for {item_id}")
                    state.complete_single_item_request(item_id, data)
                    send_message({"status": "acknowledged"})

                elif msg_type == "folderData":
                    folder_path = received_message.get("folderPath")
                    data = received_message.get("data", [])
                    logging.info(f"Received folder data for {folder_path}: {len(data)} items")
                    state.complete_folder_request(folder_path, data)
                    send_message({"status": "acknowledged"})

                elif msg_type == "markReadResult":
                    item_id = received_message.get("itemId")
                    success = received_message.get("success", False)
                    logging.info(f"Mark read result for {item_id}: {success}")
                    state.complete_mark_read_request(item_id, success)
                    send_message({"status": "acknowledged"})

            elif received_message == "ping":
                send_message("pong")

        except KeyboardInterrupt:
            break
        except Exception as e:
            logging.error(f"Error in main loop: {e}", exc_info=True)
            # Don't crash the bridge on one bad message
            time.sleep(1)


if __name__ == "__main__":
    main()

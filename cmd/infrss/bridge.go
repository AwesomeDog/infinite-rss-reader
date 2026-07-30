package main

import (
	"sync"
	"time"
)

// BridgeState manages application state and synchronization between
// the HTTP server goroutines and the main stdin loop.
type BridgeState struct {
	mu sync.Mutex

	// Cached unread RSS data
	unreadItems []interface{}
	timestamp   float64
	dataCh      chan struct{} // signaled when unread data is updated

	// Pending mark-as-read requests: itemId -> result channel
	markReadResults map[string]chan bool

	// Pending single-item requests: itemId -> result channel
	singleItemResults map[string]chan interface{}

	// Pending folder requests: folderPath -> result channel
	folderResults map[string]chan []interface{}
}

// NewBridgeState creates a new BridgeState.
func NewBridgeState() *BridgeState {
	return &BridgeState{
		dataCh:            make(chan struct{}, 1),
		markReadResults:   make(map[string]chan bool),
		singleItemResults: make(map[string]chan interface{}),
		folderResults:     make(map[string]chan []interface{}),
	}
}

// UpdateRSSData updates the cached unread items and signals waiters.
func (s *BridgeState) UpdateRSSData(data []interface{}) {
	s.mu.Lock()
	s.unreadItems = data
	s.timestamp = float64(time.Now().Unix())
	s.mu.Unlock()

	// Non-blocking signal
	select {
	case s.dataCh <- struct{}{}:
	default:
	}
}

// GetRSSData returns the currently cached unread items.
func (s *BridgeState) GetRSSData() []interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unreadItems
}

// DrainDataCh drains the data channel so the next wait is fresh.
func (s *BridgeState) DrainDataCh() {
	select {
	case <-s.dataCh:
	default:
	}
}

// --- Mark Read ---

// RegisterMarkRead creates a channel for a mark-read request.
func (s *BridgeState) RegisterMarkRead(itemID string) chan bool {
	ch := make(chan bool, 1)
	s.mu.Lock()
	s.markReadResults[itemID] = ch
	s.mu.Unlock()
	return ch
}

// CompleteMarkRead delivers the mark-read result.
func (s *BridgeState) CompleteMarkRead(itemID string, success bool) {
	s.mu.Lock()
	ch, ok := s.markReadResults[itemID]
	if ok {
		delete(s.markReadResults, itemID)
	}
	s.mu.Unlock()
	if ok {
		ch <- success
	}
}

// CleanupMarkRead removes a pending mark-read request (on timeout).
func (s *BridgeState) CleanupMarkRead(itemID string) {
	s.mu.Lock()
	delete(s.markReadResults, itemID)
	s.mu.Unlock()
}

// --- Single Item ---

// RegisterSingleItem creates a channel for a single-item request.
func (s *BridgeState) RegisterSingleItem(itemID string) chan interface{} {
	ch := make(chan interface{}, 1)
	s.mu.Lock()
	s.singleItemResults[itemID] = ch
	s.mu.Unlock()
	return ch
}

// CompleteSingleItem delivers the single-item result.
func (s *BridgeState) CompleteSingleItem(itemID string, data interface{}) {
	s.mu.Lock()
	ch, ok := s.singleItemResults[itemID]
	if ok {
		delete(s.singleItemResults, itemID)
	}
	s.mu.Unlock()
	if ok {
		ch <- data
	}
}

// CleanupSingleItem removes a pending single-item request (on timeout).
func (s *BridgeState) CleanupSingleItem(itemID string) {
	s.mu.Lock()
	delete(s.singleItemResults, itemID)
	s.mu.Unlock()
}

// --- Folder ---

// RegisterFolder creates a channel for a folder request.
func (s *BridgeState) RegisterFolder(folderPath string) chan []interface{} {
	ch := make(chan []interface{}, 1)
	s.mu.Lock()
	s.folderResults[folderPath] = ch
	s.mu.Unlock()
	return ch
}

// CompleteFolder delivers the folder result.
func (s *BridgeState) CompleteFolder(folderPath string, data []interface{}) {
	s.mu.Lock()
	ch, ok := s.folderResults[folderPath]
	if ok {
		delete(s.folderResults, folderPath)
	}
	s.mu.Unlock()
	if ok {
		ch <- data
	}
}

// CleanupFolder removes a pending folder request (on timeout).
func (s *BridgeState) CleanupFolder(folderPath string) {
	s.mu.Lock()
	delete(s.folderResults, folderPath)
	s.mu.Unlock()
}

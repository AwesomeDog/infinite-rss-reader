package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"os"
	"sync"
)

// stdoutMu serializes writes to stdout — critical because HTTP handler goroutines
// and the main loop both call sendMessage.
var stdoutMu sync.Mutex

// Native messaging max message size (Thunderbird enforces 1 MB).
const maxMessageSize = 1024 * 1024

// getMessage reads a length-prefixed JSON message from stdin.
// Returns nil on EOF or error.
func getMessage() interface{} {
	var length uint32
	err := binary.Read(os.Stdin, binary.LittleEndian, &length)
	if err != nil {
		if err != io.EOF {
			log.Printf("Error reading message length: %v", err)
		}
		return nil
	}

	if length == 0 {
		log.Printf("Invalid message length: 0")
		return nil
	}
	if length > 100*1024*1024 {
		log.Printf("WARNING: message length %d bytes exceeds 100 MB", length)
	}

	buf := make([]byte, length)
	_, err = io.ReadFull(os.Stdin, buf)
	if err != nil {
		log.Printf("Error reading message body (%d bytes): %v", length, err)
		return nil
	}

	var msg interface{}
	if err := json.Unmarshal(buf, &msg); err != nil {
		log.Printf("Error unmarshaling message: %v", err)
		return nil
	}

	return msg
}

// sendMessage writes a length-prefixed JSON message to stdout.
// It is safe to call from multiple goroutines.
func sendMessage(msg interface{}) {
	// Use json.NewEncoder with SetEscapeHTML(false) to avoid bloating
	// HTML content with \u003c / \u003e / \u0026 escapes.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(msg); err != nil {
		log.Printf("Error marshaling message: %v", err)
		return
	}

	// json.Encoder.Encode appends a trailing newline — strip it.
	data := buf.Bytes()
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}

	if len(data) > maxMessageSize {
		log.Printf("WARNING: message size (%d bytes) exceeds 1 MB limit", len(data))
	}

	// Build the complete wire frame (4-byte LE length + JSON body).
	length := uint32(len(data))
	frame := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(frame[:4], length)
	copy(frame[4:], data)

	stdoutMu.Lock()
	defer stdoutMu.Unlock()

	if _, err := os.Stdout.Write(frame); err != nil {
		log.Printf("Error writing message: %v", err)
	}
}

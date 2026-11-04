// History of the messages sent to and received from the trap. It is kept on disk so
// management-interface can show what has happened on a trap that has been out in the field
// for a while.

package comms

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// trapHistoryPath is read by management-interface. Each line is a TrapHistoryEntry as JSON,
	// oldest first.
	trapHistoryPath = "/var/lib/tc2-hat-controller/trap-history.jsonl"
	// trapHistoryMax is how many entries are kept. The file is trimmed back to this once it
	// is half as big again, so it isn't rewritten on every message.
	trapHistoryMax = 2000
	// trapHistoryMaxPayload is how much of a payload is kept, so one big message can't
	// make the file large.
	trapHistoryMaxPayload = 500
)

type TrapHistoryEntry struct {
	Time time.Time `json:"time"`
	// Dir is "sent" or "received", "failed" for a sent message that got no response, or
	// "lost" for a line from the trap that couldn't be read. For failed and lost the payload
	// is the error.
	Dir string `json:"dir"`
	// ID matches a response to the message it answers. 0 for a message the trap sent on
	// its own.
	ID      int    `json:"id,omitempty"`
	Type    string `json:"type"`
	Payload string `json:"payload,omitempty"`
}

func newHistoryEntry(dir string, msg *Message) TrapHistoryEntry {
	return TrapHistoryEntry{Time: time.Now(), Dir: dir, ID: msg.ID, Type: msg.Type, Payload: msg.Payload}
}

type trapHistory struct {
	mu    sync.Mutex
	path  string
	max   int
	lines int // Number of entries in the file, -1 until it has been counted.
}

var history = &trapHistory{path: trapHistoryPath, max: trapHistoryMax, lines: -1}

// Add appends an entry to the history.
func (h *trapHistory) Add(entry TrapHistoryEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(h.path), 0755); err != nil {
		return err
	}
	if h.lines < 0 {
		lines, err := h.read()
		if err != nil {
			return err
		}
		h.lines = len(lines)
	}

	if len(entry.Payload) > trapHistoryMaxPayload {
		entry.Payload = fmt.Sprintf("%s... (%d bytes)", entry.Payload[:trapHistoryMaxPayload], len(entry.Payload))
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	h.lines++

	if h.lines > h.max+h.max/2 {
		return h.trim()
	}
	return nil
}

// read returns the lines in the history file, or none if there isn't one yet.
func (h *trapHistory) read() ([][]byte, error) {
	data, err := os.ReadFile(h.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			lines = append(lines, bytes.Clone(scanner.Bytes()))
		}
	}
	return lines, scanner.Err()
}

// trim keeps only the newest max entries. It writes to a temporary file and renames it, so
// a power cut part way through doesn't lose the history.
func (h *trapHistory) trim() error {
	lines, err := h.read()
	if err != nil {
		return err
	}
	if len(lines) > h.max {
		lines = lines[len(lines)-h.max:]
	}
	tmp := h.path + ".tmp"
	data := append(bytes.Join(lines, []byte("\n")), '\n')
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, h.path); err != nil {
		return err
	}
	h.lines = len(lines)
	return nil
}

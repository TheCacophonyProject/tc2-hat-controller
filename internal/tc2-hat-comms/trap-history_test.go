package comms

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func readHistory(t *testing.T, h *trapHistory) []TrapHistoryEntry {
	lines, err := h.read()
	require.NoError(t, err)
	entries := []TrapHistoryEntry{}
	for _, line := range lines {
		var e TrapHistoryEntry
		require.NoError(t, json.Unmarshal(line, &e))
		entries = append(entries, e)
	}
	return entries
}

func newTestHistory(t *testing.T, max int) *trapHistory {
	return &trapHistory{path: filepath.Join(t.TempDir(), "state", "trap-history.jsonl"), max: max, lines: -1}
}

func TestTrapHistoryAdd(t *testing.T) {
	h := newTestHistory(t, 10)

	require.NoError(t, h.Add(TrapHistoryEntry{Time: time.Now(), Dir: "received", Type: "TRIGGERED"}))
	require.NoError(t, h.Add(TrapHistoryEntry{Time: time.Now(), Dir: "received", Type: "CAGE_FINISHED", Payload: `{"Action": "KILL", "Result": "KILLED"}`}))
	require.NoError(t, h.Add(TrapHistoryEntry{Time: time.Now(), Dir: "sent", ID: 3, Type: "LS", Payload: strings.Repeat("a", 600)}))

	entries := readHistory(t, h)
	require.Len(t, entries, 3)
	require.Equal(t, "TRIGGERED", entries[0].Type)
	require.Equal(t, "", entries[0].Payload)
	require.Equal(t, "CAGE_FINISHED", entries[1].Type)
	require.Equal(t, `{"Action": "KILL", "Result": "KILLED"}`, entries[1].Payload)
	require.False(t, entries[1].Time.Before(entries[0].Time))
	require.Equal(t, 3, entries[2].ID)
	require.Equal(t, strings.Repeat("a", 500)+"... (600 bytes)", entries[2].Payload)
}

func TestTrapHistoryTrim(t *testing.T) {
	h := newTestHistory(t, 10)
	path := h.path
	add := func(h *trapHistory, i int) {
		require.NoError(t, h.Add(TrapHistoryEntry{Dir: "received", Type: "MOTION", Payload: fmt.Sprint(i)}))
	}

	for i := range 15 {
		add(h, i)
	}
	// Not trimmed until it is half as big again.
	require.Len(t, readHistory(t, h), 15)

	add(h, 15)
	entries := readHistory(t, h)
	require.Len(t, entries, 10)
	require.Equal(t, "6", entries[0].Payload)
	require.Equal(t, "15", entries[9].Payload)

	// A new trapHistory (after a restart) counts what is already in the file.
	h = &trapHistory{path: path, max: 10, lines: -1}
	for i := range 6 {
		add(h, 16+i)
	}
	entries = readHistory(t, h)
	require.Len(t, entries, 10)
	require.Equal(t, "12", entries[0].Payload)

	_, err := os.Stat(path + ".tmp")
	require.True(t, os.IsNotExist(err))
}

// fakeTrap answers the messages a TrapMessenger writes with the given response types, or
// fails the write if the type is "".
type fakeTrap struct {
	lines     chan []byte
	responses map[string]string
	payloads  map[string]string // Response payloads, by response type.
}

func (f *fakeTrap) write(data []byte) error {
	msg, err := ParseLine(bytes.TrimSpace(data))
	if err != nil {
		return err
	}
	respType := f.responses[msg.Type]
	if respType == "" {
		return errors.New("write failed")
	}
	resp := Message{ID: msg.ID, Type: respType, Payload: f.payloads[respType]}
	f.lines <- []byte(strings.TrimSpace(resp.ToUARTLine()))
	return nil
}

// historySummary gives each entry as "dir type id payload" to compare more easily.
func historySummary(t *testing.T, h *trapHistory) []string {
	summary := []string{}
	for _, e := range readHistory(t, h) {
		summary = append(summary, strings.TrimSpace(fmt.Sprintf("%s %s %d %s", e.Dir, e.Type, e.ID, e.Payload)))
	}
	return summary
}

func TestTrapMessengerHistory(t *testing.T) {
	trap := &fakeTrap{lines: make(chan []byte, 16), responses: map[string]string{
		"PING":      "ACK",
		"WRITE":     "ACK",
		"ENABLE":    "ACK",
		"CAGE_KILL": "NACK",
	}}
	h := newTestHistory(t, 100)
	m := &TrapMessenger{write: trap.write, lines: trap.lines, pending: map[int]chan *Message{}, History: h}
	unsolicited := make(chan *Message, 1)
	m.UnsolicitedHandler = func(msg *Message) { unsolicited <- msg }
	m.Start()

	// A message and its response.
	_, err := m.SendMessage(Message{Type: "CAGE_KILL"})
	require.NoError(t, err)

	// Quiet messages, like file chunks, aren't saved unless they fail.
	_, err = m.SendMessage(Message{Type: "WRITE", Payload: "a.py.ztmp,[\"abc\"]", quiet: true})
	require.NoError(t, err)
	trap.responses["WRITE"] = ""
	_, err = m.SendMessage(Message{Type: "WRITE", Payload: "a.py.ztmp,[\"def\"]", quiet: true})
	require.Error(t, err)

	// Only the first of a run of unanswered PINGs is saved.
	trap.responses["PING"] = ""
	for range 3 {
		require.Error(t, m.Ping())
	}
	trap.responses["PING"] = "ACK"
	require.NoError(t, m.Ping())
	require.NoError(t, m.Ping())

	// Enabling is saved with how long it lasts, renewing it only if it fails.
	_, err = m.SetEnable(true)
	require.NoError(t, err)
	_, err = m.RenewEnable()
	require.NoError(t, err)
	trap.responses["ENABLE"] = ""
	_, err = m.RenewEnable()
	require.Error(t, err)

	// A line that can't be read, then messages the trap sends on its own.
	trap.lines <- []byte("<0|TRIGG")
	trap.lines <- []byte(strings.TrimSpace((&Message{Type: "STATUS_CHANGED", Payload: `{"State": "ARMED"}`}).ToUARTLine()))
	require.Equal(t, "STATUS_CHANGED", (<-unsolicited).Type)
	trap.lines <- []byte(strings.TrimSpace((&Message{Type: "TRIGGERED"}).ToUARTLine()))
	<-unsolicited

	require.Equal(t, []string{
		"sent CAGE_KILL 1",
		"received NACK 1",
		"sent WRITE 3 a.py.ztmp,[\"def\"]",
		"failed WRITE 3 write failed",
		"sent PING 4",
		"failed PING 4 write failed",
		"sent PING 7",
		"received ACK 7",
		"sent PING 8",
		"received ACK 8",
		"sent ENABLE 9 300",
		"received ACK 9",
		"sent ENABLE 11 300",
		"failed ENABLE 11 write failed",
		`lost  0 invalid frame: "<0|TRIGG"`,
		`received STATUS_CHANGED 0 {"State": "ARMED"}`,
		"received TRIGGERED 0",
	}, historySummary(t, h))
}

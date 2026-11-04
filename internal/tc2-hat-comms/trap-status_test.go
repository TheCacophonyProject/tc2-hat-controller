package comms

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrapMessengerStatus(t *testing.T) {
	trap := &fakeTrap{lines: make(chan []byte, 16),
		responses: map[string]string{"STATUS": "STATUS_R"},
		payloads:  map[string]string{"STATUS_R": `{"State": "ARMED", "Enabled": false}`},
	}
	h := newTestHistory(t, 100)
	m := &TrapMessenger{write: trap.write, lines: trap.lines, pending: map[int]chan *Message{}, History: h}
	m.Start()

	status, err := m.Status()
	require.NoError(t, err)
	require.Equal(t, `{"State": "ARMED", "Enabled": false}`, status)

	// Older trap software NACKs it.
	trap.responses["STATUS"] = "NACK"
	_, err = m.Status()
	require.ErrorContains(t, err, "too old")

	// Polling the status isn't saved, unless it fails.
	trap.responses["STATUS"] = ""
	_, err = m.Status()
	require.Error(t, err)
	require.Equal(t, []string{"sent STATUS 3", "failed STATUS 3 write failed"}, historySummary(t, h))
}

func TestTrapConnectionStatus(t *testing.T) {
	trap := &fakeTrap{lines: make(chan []byte, 16),
		responses: map[string]string{"STATUS": "STATUS_R", "CAGE_HOLD": "ACK"},
		payloads:  map[string]string{"STATUS_R": `{"State": "ARMED"}`},
	}
	m := &TrapMessenger{write: trap.write, lines: trap.lines, pending: map[int]chan *Message{}, History: newTestHistory(t, 100)}
	m.Start()
	s := &trapService{messenger: m}

	// While connecting the status is the stage, and requests are refused.
	setStatus(trapUpdatingSoftware)
	status, dbusErr := s.Status()
	require.Nil(t, dbusErr)
	require.Equal(t, `{"Connection":"UPDATING_SOFTWARE"}`, status)
	dbusErr = s.CageHold()
	require.NotNil(t, dbusErr)
	require.Contains(t, dbusErr.Body[0], "still connecting to the trap (updating_software)")
	setStatus(trapRestarting)
	status, _ = s.Status()
	require.Equal(t, `{"Connection":"RESTARTING"}`, status)

	// Once connected the trap's own status is used, and requests go through.
	trapIsConnected(m)
	status, dbusErr = s.Status()
	require.Nil(t, dbusErr)
	require.Equal(t, `{"State": "ARMED"}`, status)
	require.Nil(t, s.CageHold())
}

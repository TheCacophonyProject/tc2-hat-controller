// Output mode: connects to and controls a trap over serial.

package comms

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"periph.io/x/conn/v3/gpio"
)

// processTrapControl communicates the trap enabled/disabled state by writing
// the "enable" variable over UART instead of setting a digital pin.
func processTrapControl(config *CommsConfig, eventSignals chan event) error {
	trapEnabled := false
	previousTrapEnabled := false
	lastProtectSpeciesSighting := time.Time{}
	lastTrapSpeciesSighting := time.Time{}
	enablingReason := ""
	disablingReason := ""

	recordingStartTime := time.Time{}
	trackStartTime := time.Time{}
	triggerAnimal := ""
	var confidence int32

	// Open the serial port so we can send/receive messages from the trap.
	port, err := serialhelper.OpenSerial(gpio.High, gpio.Low, config.BaudRate)
	if err != nil {
		return fmt.Errorf("failed to open serial port: %v", err)
	}
	defer port.Close()

	// Create the messenger that tracks sending/receiving messages
	messenger := NewUartMessenger(port)
	messenger.Start()

	for {
		now := time.Now()
		trapEnabled = config.TrapEnabledByDefault

		if lastProtectSpeciesSighting.Add(config.ProtectDuration).After(now) {
			trapEnabled = false
		} else if lastTrapSpeciesSighting.Add(config.TrapDuration).After(now) {
			trapEnabled = true
		}

		if trapEnabled != previousTrapEnabled {
			if trapEnabled {
				log.Infof("Enabling trap, reason: %s", enablingReason)
				success, err := messenger.setEnable(true)
				if err != nil {
					return fmt.Errorf("failed to enable trap: %v", err)
				}
				trapEnableTime := time.Now()
				log.Infof("Recording start time: %s", recordingStartTime.Format("15:04:05.999"))
				log.Infof("Track start time: %s", trackStartTime.Format("15:04:05.999"))
				log.Infof("TrapEnableTime: %s", trapEnableTime.Format("15:04:05.999")) // TODO, we can get better accuracy on when this actually
				timeToEnableTrap := trapEnableTime.Sub(recordingStartTime).String()
				log.Infof("Time to enable trap: %s", timeToEnableTrap)

				eventclient.AddEvent(eventclient.Event{
					Timestamp: time.Now(),
					Type:      "trapEnableCommand",
					Details: map[string]any{
						"reason":             enablingReason,
						"recordingStartTime": recordingStartTime,
						"trackStartTime":     trackStartTime,
						"trapEnableTime":     trapEnableTime,
						"timeToEnableTrap":   timeToEnableTrap,
						"animal":             triggerAnimal,
						"confidence":         confidence,
						"enableTrapSuccess":  success, // If this fails that likely means the trap is not in a state to be enabled through the UART
					},
				})
			} else {
				log.Info("Disabling trap, reason: ", disablingReason)
				success, err := messenger.setEnable(false)
				if err != nil {
					return fmt.Errorf("failed to disable trap: %v", err)
				}
				eventclient.AddEvent(eventclient.Event{
					Timestamp: time.Now(),
					Type:      "trapDisableCommand",
					Details: map[string]any{
						"reason":             disablingReason,
						"disableTrapSuccess": success,
					},
				})
			}
		}

		previousTrapEnabled = trapEnabled

		var delay = 10 * time.Second
		trapDisableTime := lastTrapSpeciesSighting.Add(config.TrapDuration)
		if trapEnabled && time.Until(trapDisableTime) < delay {
			delay = time.Until(trapDisableTime)
		}

		disablingReason = "timeout"
		enablingReason = "timeout"
		log.Debug("Waiting")
		select {
		case t := <-eventSignals:
			switch v := t.(type) {
			case trackingEvent:
				log.Debugf("Received tracking event: %+v", v)
				trackStartTime = v.TrackStartTime

				protect, animal, conf := v.Species.MatchSpeciesWithConfidence(config.ProtectSpecies)
				if protect {
					disablingReason = fmt.Sprintf("Found an %s of confidence %d that needs to be protected", animal, conf)
					log.Debug(disablingReason)
					lastProtectSpeciesSighting = time.Now()
					break
				}

				trap, animal, conf := v.Species.MatchSpeciesWithConfidence(config.TrapSpecies)
				if trap {
					enablingReason = fmt.Sprintf("Found an %s of confidence %d that needs to be trapped", animal, conf)
					triggerAnimal = animal
					confidence = conf
					log.Debug(enablingReason)
					lastTrapSpeciesSighting = time.Now()
					break
				}

				log.Debug("No animals need to be protected or trapped, not changing trap state.")

			case recordingEvent:
				log.Debugf("Received recording event: %+v", v)
				if v.Recording {
					recordingStartTime = v.Timestamp
				} else {
					recordingStartTime = time.Time{}
				}

			default:
				log.Debugf("Ignoring non tracking event: %+v", t)
				continue
			}

		case <-time.After(delay):
			log.Debug("Scheduled check")
		}
	}
}

// Message represents the data structure for communication with a device connected on UART.
// - ID: Identifier of the message being sent or the message being responded to.
// - Response: Indicates if the message is a response.
// - Type: Specifies the type of message (e.g., write, read, command, ACK, NACK).
// - Data: Contains the actual data payload, which varies depending on the type or response.
type Message struct {
	ID                 int
	Type               string
	Payload            string
	PayloadUnmarshaled any
}

func (u *Message) String() string {
	if u.PayloadUnmarshaled != nil {
		return fmt.Sprintf("ID: %d, Type: %s, Payload: %v, PayloadUnmarshaled: %v", u.ID, u.Type, u.Payload, u.PayloadUnmarshaled)
	}
	return fmt.Sprintf("ID: %d, Type: %s, Payload: %v", u.ID, u.Type, u.Payload)
}

func (m *Message) ToUARTLine() string {
	if m == nil {
		return ""
	}
	if m.PayloadUnmarshaled != nil {
		marshaledPayload, err := json.Marshal(m.PayloadUnmarshaled)
		if err != nil {
			return ""
		}
		m.PayloadUnmarshaled = nil
		m.Payload = string(marshaledPayload)
	}
	messageStr := fmt.Sprintf("<%d|%s|%s>", m.ID, m.Type, m.Payload)
	return fmt.Sprintf("%s%d\n", messageStr, computeChecksum([]byte(messageStr)))
}

func (m *Message) Response() bool {
	return m.ID != 0
}

type Command struct {
	Command string `json:"command"`
	Args    string `json:"args,omitempty"`
}

type Write struct {
	Var string `json:"var,omitempty"`
	Val any    `json:"val,omitempty"`
}

// UartMessenger manages bidirectional communication with the RP2040 over UART.
// It holds a persistent serial port and routes incoming messages to either
// pending response waiters (matched by ID) or an unsolicited message channel.
type UartMessenger struct {
	port      *serialhelper.SerialPort
	pendingMu sync.Mutex
	pending   map[int]chan *Message
	nextID    int
	baudRate  int
}

// NewUartMessenger creates a UartMessenger using an already-open SerialPort.
func NewUartMessenger(port *serialhelper.SerialPort) *UartMessenger {
	return &UartMessenger{
		port:    port,
		pending: make(map[int]chan *Message),
	}
}

// Start begins the background routing goroutine. Unsolicited messages from the RP2040
// (i.e. not responses to a request we sent) are delivered to the unsolicited channel.
// Pass nil to discard unsolicited messages.
func (u *UartMessenger) Start() {
	go u.routeMessages()
}

// routeMessages reads lines from the serial port, parses them, and routes them:
// TODO: Maybe separate this for routing messages
// - Response messages are matched to a pending sendMessage call by ID.
// - If not a response then it is a notification from the trap.
func (u *UartMessenger) routeMessages() {
	for line := range u.port.Lines {
		// Parse the line
		msg, err := ParseLine(line)
		if err != nil {
			log.Warnf("Failed to parse incoming message %q: %v", line, err)
			continue
		}

		// Check if the message was a response
		if msg.Response() {
			u.pendingMu.Lock()
			ch, ok := u.pending[msg.ID]
			if !ok && len(u.pending) == 1 {
				// Fallback for RP2040 firmware that doesn't echo message IDs yet.
				for _, c := range u.pending {
					ch = c
					ok = true
					break
				}
			}
			u.pendingMu.Unlock()
			if ok {
				ch <- msg
				continue
			}
		}

		// If not a response then it is a notification from the trap.
		parseMessageFromTrap(msg)
	}
}

func parseMessageFromTrap(msg *Message) {
	log.Printf("Trap message: %+v", msg)

	// eventMessages maps trap message type to event type.
	// For these events we will just make an event of the given type and add the payload in the details.
	eventMessages := map[string]string{
		"MOTION":      "trapMotion",
		"ENABLED":     "trapEnabled",
		"DISABLED":    "trapDisabled",
		"SPOOL_RESET": "trapSpoolReset",
		"TRIGGERED":   "trapTriggered",
		"RUNNING":     "trapRunning",
		"ERROR_CODE":  "trapErrorCode",
		"EXCEPTION":   "trapException",
	}

	// Messages that we want to trigger the events to be uploaded right away.
	uploadEventsNowMessages := []string{
		"TRIGGERED",
		"EXCEPTION",
		"ERROR_CODE",
	}

	// Handle messages that we want to make events for
	if event, ok := eventMessages[msg.Type]; ok {
		log.Info("Making event for: ", msg.Type)
		details := map[string]any{}
		if msg.Payload != "" {
			// Try to unmarshal the payload, if not just use it as a string
			err := json.Unmarshal([]byte(msg.Payload), &details)
			if err != nil {
				details["Payload"] = msg.Payload
			}
		}
		err := eventclient.AddEvent(eventclient.Event{
			Timestamp: time.Now(),
			Type:      event,
			Details:   details,
		})
		if err != nil {
			log.Error("Error adding event:", err)
		}
		if contains(uploadEventsNowMessages, msg.Type) {
			log.Info("Uploading events now")
			err := eventclient.UploadEvents()
			if err != nil {
				log.Error("Error requesting events to be uploaded:", err)
			}
		}
		return
	}

	// Messages that we just want to make a log for, no event.
	// logMessages := []string{}
	// if contains(logMessages, msg.Type) {
	// 	log.Infof("Trap message: %+v", msg)
	// 	return
	// }

	// Unknown messages
	log.Warnf("Unknown trap message: %+v", msg)
}

func contains(arr []string, item string) bool {
	for _, v := range arr {
		if v == item {
			return true
		}
	}
	return false
}

// ParseLine parses a framed line of the form <id|type|payload>checksum.
func ParseLine(line []byte) (*Message, error) {
	line = bytes.TrimLeft(line, "\x00")
	lastIdx := bytes.LastIndexByte(line, '>')
	if lastIdx < 0 || len(line) == 0 || line[0] != '<' {
		return nil, fmt.Errorf("invalid frame: %q", line)
	}
	messageStr := line[:lastIdx+1]
	checksumStr := line[lastIdx+1:]

	receivedChecksum, err := strconv.Atoi(string(checksumStr))
	if err != nil {
		return nil, fmt.Errorf("invalid checksum in %q: %v", line, err)
	}
	if computeChecksum(messageStr) != receivedChecksum {
		return nil, fmt.Errorf("checksum mismatch in %q", line)
	}

	inner := messageStr[1 : len(messageStr)-1]
	parts := bytes.SplitN(inner, []byte("|"), 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid format: %q", line)
	}

	id, err := strconv.Atoi(string(parts[0]))
	if err != nil {
		return nil, fmt.Errorf("invalid id in %q: %v", line, err)
	}

	payload := ""
	if len(parts) == 3 {
		payload = string(parts[2])
	}

	return &Message{
		ID:      id,
		Type:    string(parts[1]),
		Payload: payload,
	}, nil
}

func computeChecksum(message []byte) int {
	checksum := 0
	for _, b := range message {
		checksum += int(b)
	}
	return checksum % 256
}

// sendMessage sends a request and waits for a matching response.
// It assigns a unique ID to the message for correlation.
func (u *UartMessenger) sendMessage(message Message) (*Message, error) {
	u.pendingMu.Lock()
	u.nextID++
	id := u.nextID
	message.ID = id
	ch := make(chan *Message, 1)
	u.pending[id] = ch
	u.pendingMu.Unlock()

	defer func() {
		u.pendingMu.Lock()
		delete(u.pending, id)
		u.pendingMu.Unlock()
	}()

	line := message.ToUARTLine()
	log.Infof("Message: '%s'", line)

	if err := u.port.Write([]byte(line)); err != nil {
		return nil, err
	}

	select {
	case response := <-ch:
		log.Println("Response:", response)
		return response, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("timeout waiting for response to message ID %d", id)
	}
}

func (u *UartMessenger) setEnable(enable bool) (bool, error) {
	message := Message{}
	if enable {
		message.Type = "ENABLE"
	} else {
		message.Type = "DISABLE"
	}
	response, err := u.sendMessage(message)
	if err != nil {
		return false, err
	}
	if response.Type == "NACK" {
		return false, fmt.Errorf("NACK response")
	}
	if response.Type == "BAD_KEY" {
		log.Warn("Got BAD_KEY response, was trying to set a key that doesn't exist")
		return false, nil
	}
	return true, nil
}

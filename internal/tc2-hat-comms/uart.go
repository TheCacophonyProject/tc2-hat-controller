// This section deals with communication with peripherals over uart.

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
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
)

// UartMessage represents the data structure for communication with a device connected on UART.
// - ID: Identifier of the message being sent or the message being responded to.
// - Response: Indicates if the message is a response.
// - Type: Specifies the type of message (e.g., write, read, command, ACK, NACK).
// - Data: Contains the actual data payload, which varies depending on the type or response.
type UartMessage struct {
	ID       int    `json:"id,omitempty"`
	Response bool   `json:"response,omitempty"`
	Type     string `json:"type,omitempty"`
	Data     any    `json:"data,omitempty"`
}

type Command struct {
	Command string `json:"command"`
	Args    string `json:"args,omitempty"`
}

type Write struct {
	Var string `json:"var,omitempty"`
	Val any    `json:"val,omitempty"`
}

// TODO: Change how the message is formatted so it is more concise. Something like: <ID,Type,Payload,CRC>
//	- ID is a uint16
//	- Type is a string (ACK, NACK, Read, Write, Command, Response...)
//	- Payload is as many bytes as needed
//	- CRC is 2 byte checksum

// UartMessenger manages bidirectional communication with the RP2040 over UART.
// It holds a persistent serial port and routes incoming messages to either
// pending response waiters (matched by ID) or an unsolicited message channel.
type UartMessenger struct {
	port      *serialhelper.SerialPort
	pendingMu sync.Mutex
	pending   map[int]chan *UartMessage
	nextID    int
}

// NewUartMessenger creates a UartMessenger using an already-open SerialPort.
func NewUartMessenger(port *serialhelper.SerialPort) *UartMessenger {
	return &UartMessenger{
		port:    port,
		pending: make(map[int]chan *UartMessage),
	}
}

// Start begins the background routing goroutine. Unsolicited messages from the RP2040
// (i.e. not responses to a request we sent) are delivered to the unsolicited channel.
// Pass nil to discard unsolicited messages.
func (u *UartMessenger) Start() {
	go u.routeMessages()
}

// routeMessages reads lines from the serial port, parses them, and routes them:
// - Response messages are matched to a pending sendMessage call by ID.
// - If not a response then it is a notification from the trap.
func (u *UartMessenger) routeMessages() {
	for line := range u.port.Lines {
		// Parse the line
		msg, err := parseLine(line)
		if err != nil {
			log.Warnf("Failed to parse incoming message %q: %v", line, err)
			continue
		}

		// Check if the message was a response
		if msg.Response {
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
		err = parseNotification(msg)
		if err != nil {
			log.Warnf("Failed to parse notification %q: %v", line, err)
			continue
		}
	}
}

func parseNotification(msg *UartMessage) error {
	// Decide on what to do with the notification
	log.Printf("notification: %+v", msg)
	if msg.Type == "spoolStatus" && msg.Data == "releasing" {
		log.Println("Spool released. Making event")
		eventclient.AddEvent(eventclient.Event{
			Timestamp: time.Now(),
			Type:      "spoolReleased",
			Details: map[string]any{
				eventclient.SeverityKey: eventclient.SeverityInfo,
			},
		})
	}
	return nil
}

// parseLine parses a framed line of the form <json|checksum>.
func parseLine(line []byte) (*UartMessage, error) {
	if len(line) < 2 || line[0] != '<' || line[len(line)-1] != '>' {
		return nil, fmt.Errorf("invalid frame: %q", line)
	}
	inner := line[1 : len(line)-1]
	parts := bytes.Split(inner, []byte("|"))
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid format: %q", line)
	}
	receivedChecksum, err := strconv.Atoi(string(parts[1]))
	if err != nil {
		return nil, fmt.Errorf("invalid checksum in %q: %v", line, err)
	}
	if computeChecksum(parts[0]) != receivedChecksum {
		return nil, fmt.Errorf("checksum mismatch in %q", line)
	}
	msg := &UartMessage{}
	return msg, json.Unmarshal(parts[0], msg)
}

func processUart(config *CommsConfig, testClassification *TestClassification, trackingSignals chan event, messenger *UartMessenger) error {
	if testClassification != nil {
		log.Println("Sending a test classification over UART")

		species := tracks.Species{
			testClassification.Animal: int32(testClassification.Confidence),
		}

		classificationData := ClassificationData{
			Species:    species,
			Confidence: int32(testClassification.Confidence),
		}

		message := UartMessage{
			Type: "classification",
			Data: classificationData,
		}
		payload, err := json.Marshal(message)
		if err != nil {
			return err
		}

		log.Printf("Sending payload: '%s'", payload)
		return messenger.port.Write(append(payload, '\r', '\n'))
	}

	for {
		log.Debug("Waiting")
		for e := range trackingSignals {
			switch v := e.(type) {
			case trackingEvent:
				fmt.Println("Tracking event:", v.Species)
				err := messenger.processTrackingEvent(v)
				if err != nil {
					log.Error("Error processing tracking event:", err)
				}
			default:
				log.Debug("Not processing event:", v)
				continue
			}
		}
	}
}

func (u *UartMessenger) processTrackingEvent(t trackingEvent) error {
	log.Debugf("Found new track: %+v", t)

	species := tracks.Species{}
	for k, v := range t.Species {
		if v > 0 {
			species[k] = v
		}
	}

	message := UartMessage{
		Type: "classification",
		Data: ClassificationData{
			Species:    species,
			Confidence: t.Confidence,
		},
	}

	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}

	log.Printf("Sending payload: '%s'", payload)
	start := time.Now()

	err = u.port.Write(append(payload, '\r', '\n'))

	log.Printf("Sent payload in %s", time.Since(start))
	return err
}

type ClassificationData struct {
	Species    tracks.Species
	Confidence int32
}

func (u *UartMessenger) sendWriteMessage(varName string, val any) error {
	data, err := json.Marshal(&Write{
		Var: varName,
		Val: val,
	})
	if err != nil {
		return err
	}
	message := UartMessage{
		Type: "write",
		Data: string(data),
	}
	response, err := u.sendMessage(message)
	if err != nil {
		return err
	}
	if response.Type == "NACK" {
		return fmt.Errorf("NACK response")
	}
	return nil
}

func (u *UartMessenger) beep() error {
	log.Println("beep")
	return u.sendCommandMessage("beep")
}

func (u *UartMessenger) sendCommandMessage(cmd string) error {
	data, err := json.Marshal(&Command{
		Command: cmd,
	})
	if err != nil {
		return err
	}
	message := UartMessage{
		Type: "command",
		Data: string(data),
	}
	response, err := u.sendMessage(message)
	if err != nil {
		return err
	}
	if response.Type == "NACK" {
		return fmt.Errorf("NACK response")
	}
	return nil
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
func (u *UartMessenger) sendMessage(cmd UartMessage) (*UartMessage, error) {
	u.pendingMu.Lock()
	u.nextID++
	id := u.nextID
	cmd.ID = id
	ch := make(chan *UartMessage, 1)
	u.pending[id] = ch
	u.pendingMu.Unlock()

	defer func() {
		u.pendingMu.Lock()
		delete(u.pending, id)
		u.pendingMu.Unlock()
	}()

	cmdData, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	message := fmt.Sprintf("<%s|%d>\n", cmdData, computeChecksum(cmdData))
	log.Infof("Message: '%s'", message)

	if err := u.port.Write([]byte(message)); err != nil {
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

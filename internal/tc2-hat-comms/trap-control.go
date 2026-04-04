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

const (
	enableString = "enable" // We set this to true/false to enable/disable the trap
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
				if err := messenger.sendWriteMessage(enableString, true); err != nil {
					return fmt.Errorf("failed to write enable=true: %v", err)
				}
				trapEnableTime := time.Now()
				log.Infof("Recording start time: %s", recordingStartTime.Format("15:04:05.999"))
				log.Infof("Track start time: %s", trackStartTime.Format("15:04:05.999"))
				log.Infof("TrapEnableTime: %s", trapEnableTime.Format("15:04:05.999")) // TODO, we can get better accuracy on when this actually
				timeToEnableTrap := trapEnableTime.Sub(recordingStartTime).String()
				log.Infof("Time to enable trap: %s", timeToEnableTrap)

				eventclient.AddEvent(eventclient.Event{
					Timestamp: time.Now(),
					Type:      "enablingTrap",
					Details: map[string]any{
						"reason":             enablingReason,
						"recordingStartTime": recordingStartTime,
						"trackStartTime":     trackStartTime,
						"trapEnableTime":     trapEnableTime,
						"timeToEnableTrap":   timeToEnableTrap,
						"animal":             triggerAnimal,
						"confidence":         confidence,
					},
				})
			} else {
				log.Info("Disabling trap, reason: ", disablingReason)
				if err := messenger.sendWriteMessage(enableString, false); err != nil {
					return fmt.Errorf("failed to write enable=false: %v", err)
				}
				eventclient.AddEvent(eventclient.Event{
					Timestamp: time.Now(),
					Type:      "disablingTrap",
					Details: map[string]any{
						"reason": disablingReason,
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
//   - ID is a uint16
//   - Type is a string (ACK, NACK, Read, Write, Command, Response...)
//   - Payload is as many bytes as needed
//   - CRC is 2 byte checksum

// UartMessenger manages bidirectional communication with the RP2040 over UART.
// It holds a persistent serial port and routes incoming messages to either
// pending response waiters (matched by ID) or an unsolicited message channel.
type UartMessenger struct {
	port      *serialhelper.SerialPort
	pendingMu sync.Mutex
	pending   map[int]chan *UartMessage
	nextID    int
	baudRate  int
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
// TODO: Maybe separate this for routing messages
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
	log.Printf("notification: %+v", msg)
	if msg.Type == "spoolStatus" && msg.Data == "releasing" {
		log.Println("Spool released. Making event")
		eventclient.AddEvent(eventclient.Event{
			Timestamp: time.Now(),
			Type:      "spoolReleased",
			Details: map[string]any{
				"timestamp": time.Now(),
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

func (u *UartMessenger) beep() error {
	log.Println("beep")
	return u.sendCommandMessage("beep")
}

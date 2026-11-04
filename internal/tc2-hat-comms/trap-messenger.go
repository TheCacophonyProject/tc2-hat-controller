package comms

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
)

// TrapMessenger manages bidirectional communication with the RP2040 over UART.
// It holds a persistent serial port and routes incoming messages to either
// pending response waiters (matched by ID) or an unsolicited message handler.
type TrapMessenger struct {
	write              func([]byte) error // Writes to the serial port.
	lines              <-chan []byte      // Lines read from the serial port.
	sendMu             sync.Mutex         // Serializes request/response exchanges so only one is in flight at a time.
	pendingMu          sync.Mutex
	pending            map[int]chan *Message
	nextID             int
	UnsolicitedHandler func(*Message)
	// History, if set, is where every message sent to and received from the trap is saved.
	History     *trapHistory
	pingFailing bool // The last PING got no reply. Guarded by sendMu.
	// OnUpload, if set, is called for each file CopyData finds needs uploading.
	OnUpload func()
}

// NewTrapMessenger creates a TrapMessenger using an already-open SerialPort.
func NewTrapMessenger(port *serialhelper.SerialPort) *TrapMessenger {
	return &TrapMessenger{
		write:   port.Write,
		lines:   port.Lines,
		pending: make(map[int]chan *Message),
	}
}

// Start begins the background routing goroutine.
func (u *TrapMessenger) Start() {
	go u.routeMessages()
}

func (u *TrapMessenger) routeMessages() {
	for line := range u.lines {
		msg, err := ParseLine(line)
		if err != nil {
			log.Warnf("Failed to parse incoming message %q: %v", line, err)
			// Save that a line was lost, it could have been a response or a message from the
			// trap. The error includes the line as it was received.
			u.record(TrapHistoryEntry{Time: time.Now(), Dir: "lost", Payload: err.Error()})
			continue
		}

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
				// SendMessage saves the response to the history, after the message it answers.
				ch <- msg
				continue
			}
		}

		u.record(newHistoryEntry("received", msg))
		if u.UnsolicitedHandler != nil {
			u.UnsolicitedHandler(msg)
		} else {
			log.Warnf("Received unsolicited message: %v", msg)
		}
	}
}

// SendMessage sends a request and waits for a matching response.
// It assigns a unique ID to the message for correlation.
// Only one exchange is allowed at a time so that messages from different goroutines,
// such as the trap control loop and the DBus service, don't get interleaved.
func (u *TrapMessenger) SendMessage(message Message) (*Message, error) {
	u.sendMu.Lock()
	defer u.sendMu.Unlock()

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
	log.Debugf("Message: '%s'", line)

	// Quiet messages, such as the chunks of a file being uploaded, would fill the history,
	// so they and their responses are only saved if they fail. While the trap isn't
	// answering, PING is sent every few seconds, so only the first PING without a reply is
	// saved.
	quiet := message.quiet
	ping := message.Type == "PING"
	sent := newHistoryEntry("sent", &message)
	if !quiet && !ping {
		u.record(sent)
	}

	response, err := u.exchange(line, ch, id)
	if err != nil {
		if !(ping && u.pingFailing) {
			if quiet || ping {
				u.record(sent)
			}
			failed := newHistoryEntry("failed", &message)
			failed.Payload = err.Error()
			u.record(failed)
		}
		if ping {
			u.pingFailing = true
		}
		return nil, err
	}
	if ping {
		u.pingFailing = false
		u.record(sent)
	}
	if !quiet {
		u.record(newHistoryEntry("received", response))
	}
	return response, nil
}

// exchange writes a message to the trap and waits for its response on ch.
func (u *TrapMessenger) exchange(line string, ch chan *Message, id int) (*Message, error) {
	if err := u.write([]byte(line)); err != nil {
		return nil, err
	}

	select {
	case response := <-ch:
		log.Debug("Response:", response)
		return response, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("timeout waiting for response to message ID %d", id)
	}
}

// record saves a message to the history, if the messenger has one.
func (u *TrapMessenger) record(entry TrapHistoryEntry) {
	if u.History == nil {
		return
	}
	if err := u.History.Add(entry); err != nil {
		log.Errorf("Failed to save trap message to history: %v", err)
	}
}

func (u *TrapMessenger) Ping() error {
	resp, err := u.SendMessage(Message{Type: "PING"})
	if err != nil {
		return err
	}
	if resp.Type != "ACK" {
		return fmt.Errorf("unexpected ping response: %s", resp.Type)
	}
	return nil
}

// SetEnable enables or disables the trap. An enable only lasts for trapEnableDuration, so
// while the trap should stay enabled it needs to be renewed with RenewEnable.
func (u *TrapMessenger) SetEnable(enable bool) (bool, error) {
	message := Message{Type: "DISABLE"}
	if enable {
		message = enableMessage()
	}
	return u.sendEnable(message)
}

// RenewEnable keeps the trap enabled for another trapEnableDuration. It is sent often, so it
// is only saved to the history if it fails.
func (u *TrapMessenger) RenewEnable() (bool, error) {
	message := enableMessage()
	message.quiet = true
	return u.sendEnable(message)
}

func enableMessage() Message {
	return Message{Type: "ENABLE", Payload: strconv.Itoa(int(trapEnableDuration.Seconds()))}
}

func (u *TrapMessenger) sendEnable(message Message) (bool, error) {
	response, err := u.SendMessage(message)
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

func HandleResponse(response *Message, err error) error {
	if err != nil {
		return err
	}
	if response.Type == "NACK" {
		return fmt.Errorf("NACK response: %s", response.Payload)
	}
	log.Infof("Response: type=%s, payload=%s", response.Type, response.Payload)
	return nil
}

func (u *TrapMessenger) Restart() error {
	return HandleResponse(u.SendMessage(Message{Type: "RESTART"}))
}

// Stop asks the trap to stop whatever it is doing, interrupting a spool or door that is
// part way through moving. Like the other manual requests this puts the trap into manual
// mode, so it stays stopped until it is restarted. The trap reports that everything has
// stopped with a STOPPED message.
func (u *TrapMessenger) Stop() error {
	return HandleResponse(u.manualRequest("STOP"))
}

// ReleaseSpool asks the trap to release the spool.
// This puts the trap into manual mode, where it stops running its sequence until it is
// restarted. The response only says the request was accepted; the trap reports that it
// has released with a TRIGGERED message.
func (u *TrapMessenger) ReleaseSpool() error {
	return HandleResponse(u.manualRequest("RELEASE_SPOOL"))
}

// ResetSpool asks the trap to reset the spool.
// Like ReleaseSpool this puts the trap into manual mode, and the trap reports that it
// has reset with a SPOOL_RESET message, or SPOOL_RESET_FAILED if its reed switch shows
// the spool didn't stay reset.
func (u *TrapMessenger) ResetSpool() error {
	return HandleResponse(u.manualRequest("RESET_SPOOL"))
}

// OpenDoor asks the trap to open one of its ratchet doors, ratcheting it up and holding
// it there. Like the spool requests this puts the trap into manual mode, and the response
// only says the request was accepted; the trap reports that the door has finished moving
// with a DOOR_OPENED message. Opening takes around 20 seconds.
func (u *TrapMessenger) OpenDoor(door int) error {
	if door != 1 && door != 2 {
		return fmt.Errorf("no such door: %d", door)
	}
	return HandleResponse(u.manualRequest(fmt.Sprintf("OPEN_DOOR_%d", door)))
}

// CloseDoor asks the trap to close one of its ratchet doors, releasing the ratchet so the
// door drops. Like OpenDoor this puts the trap into manual mode, and the trap reports that
// the door has finished moving with a DOOR_CLOSED message.
func (u *TrapMessenger) CloseDoor(door int) error {
	if door != 1 && door != 2 {
		return fmt.Errorf("no such door: %d", door)
	}
	return HandleResponse(u.manualRequest(fmt.Sprintf("CLOSE_DOOR_%d", door)))
}

// Cage commands, for an animal held in the cage on a trap that has one fitted.
// Unlike the manual requests these don't put the trap into manual mode, they steer the
// cage sequence. Each is only accepted in part of the sequence, the trap NACKs, with the
// reason in the payload, a command that doesn't apply.

// CageHold asks the trap to keep holding the animal in the cage, for another
// cage_hold_hours from now. Without one the trap releases the animal once that has passed.
// The trap reports the new hold with a CAGE_HOLDING message.
func (u *TrapMessenger) CageHold() error {
	return HandleResponse(u.cageRequest("CAGE_HOLD"))
}

// CageRelease asks the trap to let the animal out of the cage, back through the spool.
// The trap reports this with a CAGE_RELEASING message.
func (u *TrapMessenger) CageRelease() error {
	return HandleResponse(u.cageRequest("CAGE_RELEASE"))
}

// CageKill asks the trap to open the cage door to the kill mechanism.
// The trap reports this with a CAGE_KILLING message.
func (u *TrapMessenger) CageKill() error {
	return HandleResponse(u.cageRequest("CAGE_KILL"))
}

// CageWait asks the trap, once it has released the animal or opened the door to the kill
// mechanism, to wait another cage_exit_wait_hours for the animal to leave the cage.
// The trap reports the new wait with a CAGE_WAITING message.
func (u *TrapMessenger) CageWait() error {
	return HandleResponse(u.cageRequest("CAGE_WAIT"))
}

// CageEscaped tells the trap the animal has left the cage, so it can carry on.
// The trap reports this with a CAGE_FINISHED message.
func (u *TrapMessenger) CageEscaped() error {
	return HandleResponse(u.cageRequest("CAGE_ESCAPED"))
}

// CageKilled tells the trap the animal has been killed, so it can carry on.
// The trap reports this with a CAGE_FINISHED message.
func (u *TrapMessenger) CageKilled() error {
	return HandleResponse(u.cageRequest("CAGE_KILLED"))
}

// cageRequest sends one of the cage commands.
func (u *TrapMessenger) cageRequest(messageType string) (*Message, error) {
	response, err := u.SendMessage(Message{Type: messageType})
	if err != nil {
		return nil, err
	}
	if response.Type == "BAD_KEY" {
		return nil, fmt.Errorf("the trap doesn't have a cage enabled, or the program running on it doesn't support one")
	}
	return response, nil
}

// manualRequest sends a request that hands the trap over to manual mode.
func (u *TrapMessenger) manualRequest(messageType string) (*Message, error) {
	response, err := u.SendMessage(Message{Type: messageType})
	if err != nil {
		return nil, err
	}
	if response.Type == "BAD_KEY" {
		return nil, fmt.Errorf("the program running on the trap doesn't support manual control")
	}
	return response, nil
}

// Status asks the trap what it is doing. It returns the JSON payload of the trap's
// STATUS_R reply, with fields such as State, CagePhase, Enabled and DisabledReason (see
// status_reply in spool-py). It is quiet, so polling it doesn't fill the history. The trap
// also sends its status when it changes, which is passed on as a StatusChanged signal.
func (u *TrapMessenger) Status() (string, error) {
	response, err := u.SendMessage(Message{Type: "STATUS", quiet: true})
	if err != nil {
		return "", err
	}
	switch response.Type {
	case "STATUS_R":
		return response.Payload, nil
	case "NACK":
		// Trap software from before STATUS NACKs messages it doesn't know.
		return "", fmt.Errorf("the software on the trap is too old to report its status")
	default:
		return "", fmt.Errorf("unexpected %s response to STATUS", response.Type)
	}
}

func (u *TrapMessenger) ReadTime() error {
	return HandleResponse(u.SendMessage(Message{Type: "READ_TIME"}))
}

// SetTime sets the clock on the trap to the camera's current UTC time.
func (u *TrapMessenger) SetTime() error {
	return u.WriteTime("")
}

func (u *TrapMessenger) WriteTime(timeStr string) error {
	if timeStr == "" {
		timeStr = time.Now().UTC().Format(time.DateTime)
	}
	log.Printf("Writing UTC time: '%s'", timeStr)
	return HandleResponse(u.SendMessage(Message{Type: "WRITE_TIME", Payload: timeStr}))
}

func (u *TrapMessenger) CommitFiles() error {
	log.Println("Committing all .tmp files...")
	return HandleResponse(u.SendMessage(Message{Type: "COMMIT"}))
}

// CopyDir uploads all files from sourceDir to destDir on the RP2040, then commits them.
// Returns true if any file was updated.
// Only files that don't match the hash will be updated unless force is true.
func (u *TrapMessenger) CopyDir(sourceDir, destDir string, force bool) (bool, error) {
	aFileWasUpdated := false
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return false, fmt.Errorf("failed to read directory %s: %v", sourceDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			// TODO: recursively copy subdirectories
			continue
		}
		localFile := filepath.Join(sourceDir, entry.Name())
		destFile := filepath.Join(destDir, entry.Name())
		fileUpdated, err := u.CopyFile(localFile, destFile, force)
		if err != nil {
			return false, fmt.Errorf("failed to copy %s: %v", entry.Name(), err)
		}
		aFileWasUpdated = aFileWasUpdated || fileUpdated
	}
	return aFileWasUpdated, u.CommitFiles()
}

// CopyFile uploads a local file to the RP2040. See CopyData for the details of
// how the file is written.
func (u *TrapMessenger) CopyFile(localFile, destFile string, force bool) (bool, error) {
	localData, err := os.ReadFile(localFile)
	if err != nil {
		return false, fmt.Errorf("failed to read local file %s: %v", localFile, err)
	}
	return u.CopyData(localData, destFile, force)
}

// CopyData uploads the given data to the RP2040 as destFile.
// The data will be written to a .tmp file on the RP2040. Once you want to commit the file change use the COMMIT command.
// It returns a bool that indicates whether the file needed to be updated.
// Only data that doesn't match the hash on the trap will be uploaded unless force is true.
func (u *TrapMessenger) CopyData(localData []byte, destFile string, force bool) (bool, error) {
	destBase := filepath.Base(destFile)
	compressedBase := destBase + ".ztmp"
	tmpBase := destBase + ".tmp"
	log.Printf("Uploading '%s' as '%s'", destFile, tmpBase)

	h := sha256.Sum256(localData)
	localHash := hex.EncodeToString(h[:])[:10]

	lsResp, err := u.SendMessage(Message{Type: "LS", Payload: destBase + "," + compressedBase + "," + tmpBase})
	if err != nil {
		return false, fmt.Errorf("failed to list files: %v", err)
	}
	var fileHashes map[string]string
	if err := json.Unmarshal([]byte(lsResp.Payload), &fileHashes); err != nil {
		return false, fmt.Errorf("failed to parse LS response: %v", err)
	}

	if fileHashes[destBase] == localHash {
		log.Printf("\tFile is already up to date.")
		if !force {
			return false, nil
		}
		log.Println("\tForce flag is set, still uploading.")
	}
	if fileHashes[tmpBase] == localHash {
		log.Printf("\t.tmp file is already up to date.")
		if !force {
			return false, nil
		}
		log.Println("\tForce flag is set, still uploading.")
	}

	if u.OnUpload != nil {
		u.OnUpload()
	}

	if _, ok := fileHashes[compressedBase]; ok {
		if err := HandleResponse(u.SendMessage(Message{Type: "DELETE", Payload: compressedBase})); err != nil {
			return false, fmt.Errorf("failed to delete temp file: %v", err)
		}
	}

	var compressed bytes.Buffer
	fw, err := flate.NewWriter(&compressed, flate.HuffmanOnly)
	if err != nil {
		return false, fmt.Errorf("failed to create compressor: %v", err)
	}
	if _, err := fw.Write(localData); err != nil {
		return false, fmt.Errorf("failed to compress file: %v", err)
	}
	if err := fw.Close(); err != nil {
		return false, fmt.Errorf("failed to finalize compression: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())
	log.Infof("\t%d bytes -> %d bytes compressed (%.0f%%)", len(localData), compressed.Len(), float64(compressed.Len())/float64(len(localData))*100)

	const chunkSize = 500
	totalChunks := (len(encoded) + chunkSize - 1) / chunkSize
	for i := 0; i < len(encoded); i += chunkSize {
		chunkNum := i/chunkSize + 1
		log.Infof("\t%s: %d/%d", destBase, chunkNum, totalChunks)
		chunk, err := json.Marshal([]string{encoded[i:min(i+chunkSize, len(encoded))]})
		if err != nil {
			return false, fmt.Errorf("failed to marshal chunk: %v", err)
		}
		if err := HandleResponse(u.SendMessage(Message{Type: "WRITE", Payload: compressedBase + "," + string(chunk), quiet: true})); err != nil {
			return false, fmt.Errorf("failed to write chunk at offset %d: %v", i, err)
		}
	}

	log.Println("\tDecompressing...")
	if err := HandleResponse(u.SendMessage(Message{Type: "DECOMPRESS", Payload: compressedBase + "," + tmpBase})); err != nil {
		return false, fmt.Errorf("failed to decompress file: %v", err)
	}

	log.Println("\tVerifying...")
	lsResp2, err := u.SendMessage(Message{Type: "LS", Payload: tmpBase})
	if err != nil {
		return false, fmt.Errorf("failed to verify file: %v", err)
	}
	var fileHashes2 map[string]string
	if err := json.Unmarshal([]byte(lsResp2.Payload), &fileHashes2); err != nil {
		return false, fmt.Errorf("failed to parse verify LS response: %v", err)
	}
	if fileHashes2[tmpBase] != localHash {
		return false, fmt.Errorf("file verification failed: hash mismatch")
	}

	log.Printf("\tFile '%s' copied successfully.", tmpBase)
	return true, nil
}

func (u *TrapMessenger) DeleteTmpFiles() error {
	log.Println("Deleting all .tmp files...")
	return HandleResponse(u.SendMessage(Message{Type: "DELETE_TMP"}))
}

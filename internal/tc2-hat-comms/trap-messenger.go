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
	"sync"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
)

// TrapMessenger manages bidirectional communication with the RP2040 over UART.
// It holds a persistent serial port and routes incoming messages to either
// pending response waiters (matched by ID) or an unsolicited message handler.
type TrapMessenger struct {
	port               *serialhelper.SerialPort
	pendingMu          sync.Mutex
	pending            map[int]chan *Message
	nextID             int
	UnsolicitedHandler func(*Message)
}

// NewTrapMessenger creates a TrapMessenger using an already-open SerialPort.
func NewTrapMessenger(port *serialhelper.SerialPort) *TrapMessenger {
	return &TrapMessenger{
		port:    port,
		pending: make(map[int]chan *Message),
	}
}

// Start begins the background routing goroutine.
func (u *TrapMessenger) Start() {
	go u.routeMessages()
}

func (u *TrapMessenger) routeMessages() {
	for line := range u.port.Lines {
		msg, err := ParseLine(line)
		if err != nil {
			log.Warnf("Failed to parse incoming message %q: %v", line, err)
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
				ch <- msg
				continue
			}
		}

		if u.UnsolicitedHandler != nil {
			u.UnsolicitedHandler(msg)
		}
	}
}

// SendMessage sends a request and waits for a matching response.
// It assigns a unique ID to the message for correlation.
func (u *TrapMessenger) SendMessage(message Message) (*Message, error) {
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

	if err := u.port.Write([]byte(line)); err != nil {
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

func (u *TrapMessenger) SetEnable(enable bool) (bool, error) {
	message := Message{}
	if enable {
		message.Type = "ENABLE"
	} else {
		message.Type = "DISABLE"
	}
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

func (u *TrapMessenger) ReadTime() error {
	return HandleResponse(u.SendMessage(Message{Type: "READ_TIME"}))
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

// CopyFile uploads a file to the RP2040.
// The file will be written to a .tmp file on the RP2040. Once you want to commit the file change use the COMMIT command.
// It returns a bool that indicates whether the file needed to be updated.
// Only files that don't match the hash will be updated unless force is true.
func (u *TrapMessenger) CopyFile(localFile, destFile string, force bool) (bool, error) {
	destBase := filepath.Base(destFile)
	compressedBase := destBase + ".ztmp"
	tmpBase := destBase + ".tmp"
	log.Printf("Uploading '%s' as '%s'", destFile, tmpBase)

	localData, err := os.ReadFile(localFile)
	if err != nil {
		return false, fmt.Errorf("failed to read local file %s: %v", localFile, err)
	}

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
		log.Infof("\t%s: %d/%d", filepath.Base(localFile), chunkNum, totalChunks)
		chunk, err := json.Marshal([]string{encoded[i:min(i+chunkSize, len(encoded))]})
		if err != nil {
			return false, fmt.Errorf("failed to marshal chunk: %v", err)
		}
		if err := HandleResponse(u.SendMessage(Message{Type: "WRITE", Payload: compressedBase + "," + string(chunk)})); err != nil {
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

package trapcli

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	comms "github.com/TheCacophonyProject/tc2-hat-controller/internal/tc2-hat-comms"

	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"github.com/alexflint/go-arg"
	"periph.io/x/conn/v3/gpio"
)

var (
	version = "<not set>"
	log     = logging.NewLogger("info")
)

type Args struct {
	Command   *Command    `arg:"subcommand:command" help:"Send a command."`
	Read      *Read       `arg:"subcommand:read" help:"Read from a variable."`
	Write     *Write      `arg:"subcommand:write" help:"Write to a variable."`
	Listen    *Listen     `arg:"subcommand:listen" help:"Continuously listen for messages from the RP2040."`
	Message   *CMDMessage `arg:"subcommand:msg" help:"Send a message to the RP2040."`
	CopyFile  *CopyFile   `arg:"subcommand:copy-file" help:"Copy a file to the RP2040."`
	CopyDir   *CopyDir    `arg:"subcommand:copy-dir" help:"Copy all files from a directory to the RP2040."`
	Restart   *Restart    `arg:"subcommand:restart" help:"Restart the RP2040."`
	ReadTime  *ReadTime   `arg:"subcommand:read-time" help:"Read the time from the RP2040."`
	WriteTime *WriteTime  `arg:"subcommand:write-time" help:"Write the time to the RP2040."`
	BaudRate  int         `arg:"--baud-rate" help:"Baud rate for UART communication."`
	goconfig.ConfigArgs
	logging.LogArgs
}

type CMDMessage struct {
	ID      int    `arg:"--id,required" help:"The ID of the message to send."`
	Type    string `arg:"--type,required" help:"The type of message to send."`
	Payload string `arg:"--payload,required" help:"The payload of the message to send."`
}

type CopyFile struct {
	Source string `arg:"--source,required" help:"The source file to copy."`
	Dest   string `arg:"--dest,required" help:"The destination file to copy to."`
	Force  bool   `arg:"--force" help:"Force overwrite of the destination file."`
}

type CopyDir struct {
	Source string `arg:"--source,required" help:"The source directory to copy files from."`
	Dest   string `arg:"--dest,required" help:"The destination directory on the RP2040."`
	Force  bool   `arg:"--force" help:"Force overwrite of the files."`
}

type Command struct {
	Command string `arg:"--command,required" help:"The command to run."`
}

type Read struct {
	Variable string `arg:"--variable,required" help:"The variable to read from."`
}

type Write struct {
	Variable string `arg:"--variable,required" help:"The variable to write to."`
	Value    string `arg:"--value,required" help:"The value to write."`
}

type ReadTime struct{}

type WriteTime struct {
	Time string `arg:"--time" help:"The time to write."`
}

type Listen struct{}

type Restart struct{}

var defaultArgs = Args{
	BaudRate: 9600,
}

const maxRetries = 3

func sendMessage(msg comms.Message, port *serialhelper.SerialPort) (*comms.Message, error) {
	line := msg.ToUARTLine()
	var lastErr error
	for i := range maxRetries {
		if i > 0 {
			log.Warnf("Retrying (%d/%d): %s", i, maxRetries-1, strings.TrimSpace(line))
		} else {
			//log.Println("Sending:", strings.TrimSpace(line))
		}
		if err := port.Write([]byte(line)); err != nil {
			return nil, err
		}
		select {
		case line, ok := <-port.Lines:
			if !ok {
				return nil, fmt.Errorf("serial port closed while waiting for response")
			}
			return comms.ParseLine(line)
		case <-time.After(5 * time.Second):
			lastErr = fmt.Errorf("timeout waiting for response")
		}
	}
	return nil, lastErr
}

func procArgs(input []string) (Args, error) {
	args := defaultArgs

	parser, err := arg.NewParser(arg.Config{}, &args)
	if err != nil {
		return Args{}, err
	}
	err = parser.Parse(input)
	if errors.Is(err, arg.ErrHelp) {
		parser.WriteHelp(os.Stdout)
		os.Exit(0)
	}
	if errors.Is(err, arg.ErrVersion) {
		log.Infoln(version)
		os.Exit(0)
	}
	return args, err
}

func Run(inputArgs []string, ver string) error {
	version = ver
	args, err := procArgs(inputArgs)
	if err != nil {
		return fmt.Errorf("failed to parse args: %v", err)
	}
	log = logging.NewLogger(args.LogLevel)
	log.Printf("Running version: %s", version)

	port, err := serialhelper.OpenSerial(gpio.High, gpio.Low, args.BaudRate)
	if err != nil {
		return fmt.Errorf("failed to open serial port: %v", err)
	}
	defer port.Close()

	switch {
	case args.Listen != nil:
		log.Info("Listening for messages from RP2040 (Ctrl+C to stop)...")
		for line := range port.Lines {
			msg, err := comms.ParseLine(line)
			if err != nil {
				log.Infof("raw: %s\n", line)
				log.Warnf("Failed to parse incoming message %q: %v", line, err)
				continue
			}
			log.Println("Received:", msg)
		}
		return nil

	case args.Command != nil:
		data, err := json.Marshal(map[string]string{"command": args.Command.Command})
		if err != nil {
			return err
		}
		return respond(sendMessage(comms.Message{Type: "command", Payload: string(data)}, port))

	case args.Read != nil:
		data, err := json.Marshal(map[string]string{"var": args.Read.Variable})
		if err != nil {
			return err
		}
		return respond(sendMessage(comms.Message{Type: "read", Payload: string(data)}, port))

	case args.Write != nil:
		data, err := json.Marshal(map[string]string{"var": args.Write.Variable, "val": args.Write.Value})
		if err != nil {
			return err
		}
		return respond(sendMessage(comms.Message{Type: "write", Payload: string(data)}, port))

	case args.Message != nil:
		message := comms.Message{ID: args.Message.ID, Type: args.Message.Type, Payload: args.Message.Payload}
		return respond(sendMessage(message, port))

	case args.Restart != nil:
		return respond(sendMessage(comms.Message{Type: "RESTART"}, port))

	case args.CopyFile != nil:
		if err := copyFile(args.CopyFile.Source, args.CopyFile.Dest, port, args.CopyFile.Force); err != nil {
			return err
		}
		return commitFiles(port)

	case args.CopyDir != nil:
		entries, err := os.ReadDir(args.CopyDir.Source)
		if err != nil {
			return fmt.Errorf("failed to read directory %s: %v", args.CopyDir.Source, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			localFile := filepath.Join(args.CopyDir.Source, entry.Name())
			destFile := filepath.Join(args.CopyDir.Dest, entry.Name())
			if err := copyFile(localFile, destFile, port, args.CopyDir.Force); err != nil {
				return fmt.Errorf("failed to copy %s: %v", entry.Name(), err)
			}
		}
		return commitFiles(port)

	case args.ReadTime != nil:
		return respond(sendMessage(comms.Message{Type: "READ_TIME"}, port))

	case args.WriteTime != nil:
		timeStr := time.Now().UTC().Format(time.DateTime)
		if args.WriteTime.Time != "" {
			timeStr = args.WriteTime.Time
		}
		log.Printf("Writing UTC time: '%s'", timeStr)
		return respond(sendMessage(comms.Message{Type: "WRITE_TIME", Payload: timeStr}, port))

	default:
		return fmt.Errorf("no subcommand given")
	}
}

func commitFiles(port *serialhelper.SerialPort) error {
	log.Println("Committing all .tmp files...")
	return respond(sendMessage(comms.Message{Type: "COMMIT"}, port))
}

func respond(response *comms.Message, err error) error {
	if err != nil {
		return err
	}
	if response.Type == "NACK" {
		return fmt.Errorf("NACK response: %s", response.Payload)
	}
	log.Infof("Response: type=%s, payload=%s", response.Type, response.Payload)
	return nil
}

func copyFile(localFile, destFile string, port *serialhelper.SerialPort, force bool) error {
	destBase := filepath.Base(destFile)
	compressedBase := destBase + ".ztmp"
	tmpBase := destBase + ".tmp"
	log.Printf("Uploading '%s' as '%s'", destFile, tmpBase)

	localData, err := os.ReadFile(localFile)
	if err != nil {
		return fmt.Errorf("failed to read local file %s: %v", localFile, err)
	}

	h := sha256.Sum256(localData)
	localHash := hex.EncodeToString(h[:])[:10]

	lsResp, err := sendMessage(comms.Message{Type: "LS", Payload: destBase + "," + compressedBase + "," + tmpBase}, port)
	if err != nil {
		return fmt.Errorf("failed to list files: %v", err)
	}
	var fileHashes map[string]string
	if err := json.Unmarshal([]byte(lsResp.Payload), &fileHashes); err != nil {
		return fmt.Errorf("failed to parse LS response: %v", err)
	}

	// Check if file or tmp file is already up to date
	if fileHashes[destBase] == localHash {
		log.Printf("\tFile is already up to date.")
		if !force {
			return nil
		}
		log.Println("\tForce flag is set, still uploading.")
	}
	if fileHashes[tmpBase] == localHash {
		log.Printf("\t.tmp file is already up to date.")
		if !force {
			return nil
		}
		log.Println("\tForce flag is set, still uploading.")
	}

	// Delete old ztmp file if it exists
	if _, ok := fileHashes[compressedBase]; ok {
		if err := respond(sendMessage(comms.Message{Type: "DELETE", Payload: compressedBase}, port)); err != nil {
			return fmt.Errorf("failed to delete temp file: %v", err)
		}
	}

	var compressed bytes.Buffer
	fw, err := flate.NewWriter(&compressed, flate.HuffmanOnly)
	if err != nil {
		return fmt.Errorf("failed to create compressor: %v", err)
	}
	if _, err := fw.Write(localData); err != nil {
		return fmt.Errorf("failed to compress file: %v", err)
	}
	if err := fw.Close(); err != nil {
		return fmt.Errorf("failed to finalize compression: %v", err)
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
			return fmt.Errorf("failed to marshal chunk: %v", err)
		}
		if err := respond(sendMessage(comms.Message{Type: "WRITE", Payload: compressedBase + "," + string(chunk)}, port)); err != nil {
			return fmt.Errorf("failed to write chunk at offset %d: %v", i, err)
		}
	}

	log.Println("\tDecompressing...")
	if err := respond(sendMessage(comms.Message{Type: "DECOMPRESS", Payload: compressedBase + "," + tmpBase}, port)); err != nil {
		return fmt.Errorf("failed to decompress file: %v", err)
	}

	log.Println("\tVerifying...")
	lsResp2, err := sendMessage(comms.Message{Type: "LS", Payload: tmpBase}, port)
	if err != nil {
		return fmt.Errorf("failed to verify file: %v", err)
	}
	var fileHashes2 map[string]string
	if err := json.Unmarshal([]byte(lsResp2.Payload), &fileHashes2); err != nil {
		return fmt.Errorf("failed to parse verify LS response: %v", err)
	}
	if fileHashes2[tmpBase] != localHash {
		return fmt.Errorf("file verification failed: hash mismatch")
	}

	log.Printf("\tFile '%s' copied successfully.", tmpBase)
	return nil
}

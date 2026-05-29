package trapcli

import (
	"crypto/sha256"
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
	Command  *Command    `arg:"subcommand:command" help:"Send a command."`
	Read     *Read       `arg:"subcommand:read" help:"Read from a variable."`
	Write    *Write      `arg:"subcommand:write" help:"Write to a variable."`
	Listen   *Listen     `arg:"subcommand:listen" help:"Continuously listen for messages from the RP2040."`
	Message  *CMDMessage `arg:"subcommand:msg" help:"Send a message to the RP2040."`
	CopyFile *CopyFile   `arg:"subcommand:copy-file" help:"Copy a file to the RP2040."`
	BaudRate int         `arg:"--baud-rate" help:"Baud rate for UART communication."`
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

type Listen struct{}

var defaultArgs = Args{
	BaudRate: 9600,
}

func sendMessage(msg comms.Message, port *serialhelper.SerialPort) (*comms.Message, error) {
	line := msg.ToUARTLine()
	log.Println("Sending:", strings.TrimSpace(line))

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
		return nil, fmt.Errorf("timeout waiting for response")
	}
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
		fmt.Println(version)
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
		fmt.Println("Listening for messages from RP2040 (Ctrl+C to stop)...")
		for line := range port.Lines {
			msg, err := comms.ParseLine(line)
			if err != nil {
				fmt.Printf("raw: %s\n", line)
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

	case args.CopyFile != nil:
		localFile := args.CopyFile.Source
		destFile := args.CopyFile.Dest

		// Check that local file exists.
		localData, err := os.ReadFile(localFile)
		if err != nil {
			return fmt.Errorf("failed to read local file %s: %v", localFile, err)
		}

		// Calculate the hash of the file, just he first 10 characters.
		h := sha256.Sum256(localData)
		localHash := hex.EncodeToString(h[:])[:10]
		destBase := filepath.Base(destFile)
		tmpBase := destBase + ".tmp"

		// Check if the files already matches
		lsResp, err := sendMessage(comms.Message{Type: "LS", Payload: destBase + "," + tmpBase}, port)
		if err != nil {
			return fmt.Errorf("failed to list files: %v", err)
		}
		var fileHashes map[string]string
		if err := json.Unmarshal([]byte(lsResp.Payload), &fileHashes); err != nil {
			return fmt.Errorf("failed to parse LS response: %v", err)
		}
		if fileHashes[destBase] == localHash {
			log.Printf("File %s already up to date", destFile)
			return nil
		}

		// Delete the temp file if it already exists
		if _, ok := fileHashes[tmpBase]; ok {
			if err := respond(sendMessage(comms.Message{Type: "DELETE", Payload: tmpBase}, port)); err != nil {
				return fmt.Errorf("failed to delete temp file: %v", err)
			}
		}

		// Split the file into lines. Removing the trailing newline as that will be added by the RP2040
		lines := strings.Split(strings.TrimSuffix(string(localData), "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			lines = nil
		}

		for _, line := range lines {
			lineSend := []string{line}
			chunk, err := json.Marshal(lineSend)
			if err != nil {
				return fmt.Errorf("failed to marshal chunk: %v", err)
			}
			if err := respond(sendMessage(comms.Message{Type: "WRITE", Payload: tmpBase + "," + string(chunk)}, port)); err != nil {
				return fmt.Errorf("failed to write chunk at line %s: %v", line, err)
			}
		}

		// Verify temp file.
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

		// Move the temp file to the destination
		if err := respond(sendMessage(comms.Message{Type: "MV", Payload: tmpBase + "," + destBase}, port)); err != nil {
			return fmt.Errorf("failed to move file: %v", err)
		}

		// Verify the final file
		lsResp3, err := sendMessage(comms.Message{Type: "LS", Payload: destBase}, port)
		if err != nil {
			return fmt.Errorf("failed to verify file: %v", err)
		}
		var fileHashes3 map[string]string
		if err := json.Unmarshal([]byte(lsResp3.Payload), &fileHashes3); err != nil {
			return fmt.Errorf("failed to parse verify LS response: %v", err)
		}
		if fileHashes3[destBase] != localHash {
			return fmt.Errorf("file verification failed: hash mismatch. Got %s, expected %s", fileHashes3[tmpBase], localHash)
		}

		log.Printf("File %s copied successfully", destFile)
		return nil

	default:
		return fmt.Errorf("no subcommand given")
	}
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

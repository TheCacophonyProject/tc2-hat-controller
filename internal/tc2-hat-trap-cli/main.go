package trapcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

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
	Command  *Command `arg:"subcommand:command" help:"Send a command."`
	Read     *Read    `arg:"subcommand:read" help:"Read from a variable."`
	Write    *Write   `arg:"subcommand:write" help:"Write to a variable."`
	Listen   *Listen  `arg:"subcommand:listen" help:"Continuously listen for messages from the RP2040."`
	BaudRate int      `arg:"--baud-rate" help:"Baud rate for UART communication."`
	goconfig.ConfigArgs
	logging.LogArgs
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

type uartMessage struct {
	ID       int    `json:"id,omitempty"`
	Response bool   `json:"response,omitempty"`
	Type     string `json:"type,omitempty"`
	Data     string `json:"data,omitempty"`
}

func computeChecksum(message []byte) int {
	checksum := 0
	for _, b := range message {
		checksum += int(b)
	}
	return checksum % 256
}

func parseFrame(line []byte) (*uartMessage, error) {
	if len(line) < 2 || line[0] != '<' || line[len(line)-1] != '>' {
		return nil, fmt.Errorf("invalid frame: %q", line)
	}
	inner := line[1 : len(line)-1]
	parts := bytes.Split(inner, []byte("|"))
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid format, got %d parts from %q", len(parts), inner)
	}
	receivedChecksum, err := strconv.Atoi(string(parts[1]))
	if err != nil {
		return nil, fmt.Errorf("invalid checksum %q: %w", parts[1], err)
	}
	if computeChecksum(parts[0]) != receivedChecksum {
		return nil, fmt.Errorf("checksum mismatch in %q", line)
	}
	msg := &uartMessage{}
	return msg, json.Unmarshal(parts[0], msg)
}

func sendMessage(msg uartMessage, port *serialhelper.SerialPort) (*uartMessage, error) {
	msgData, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	frame := fmt.Sprintf("<%s|%d>\n", msgData, computeChecksum(msgData))
	log.Println("Sending:", frame)

	if err := port.Write([]byte(frame)); err != nil {
		return nil, err
	}

	select {
	case line, ok := <-port.Lines:
		if !ok {
			return nil, fmt.Errorf("serial port closed while waiting for response")
		}
		return parseFrame(line)
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
			msg, err := parseFrame(line)
			if err != nil {
				fmt.Printf("raw: %s\n", line)
				continue
			}
			fmt.Printf("type=%s data=%s\n", msg.Type, msg.Data)
		}
		return nil

	case args.Command != nil:
		data, err := json.Marshal(map[string]string{"command": args.Command.Command})
		if err != nil {
			return err
		}
		err = respond(sendMessage(uartMessage{Type: "command", Data: string(data)}, port))
		return err

	case args.Read != nil:
		data, err := json.Marshal(map[string]string{"var": args.Read.Variable})
		if err != nil {
			return err
		}
		err = respond(sendMessage(uartMessage{Type: "read", Data: string(data)}, port))
		return err

	case args.Write != nil:
		data, err := json.Marshal(map[string]string{"var": args.Write.Variable, "val": args.Write.Value})
		if err != nil {
			return err
		}
		err = respond(sendMessage(uartMessage{Type: "write", Data: string(data)}, port))
		return err

	default:
		return fmt.Errorf("no subcommand given")
	}
}

func respond(response *uartMessage, err error) error {
	if err != nil {
		return err
	}
	if response.Type == "NACK" {
		return fmt.Errorf("NACK response: %s", response.Data)
	}
	fmt.Printf("type=%s data=%s\n", response.Type, response.Data)
	return nil
}

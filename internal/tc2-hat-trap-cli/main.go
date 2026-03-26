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
	Command  *Command `arg:"subcommand" help:"Send a command."`
	Read     *Read    `arg:"subcommand:read" help:"Read from a variable."`
	Write    *Write   `arg:"subcommand:write" help:"Write to a variable."`
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

func sendMessage(msg uartMessage, baudRate int) (*uartMessage, error) {
	// Generate message
	msgData, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	frame := fmt.Sprintf("<%s|%d>\n", msgData, computeChecksum(msgData))
	log.Println("Sending:", frame)
	log.Println(len(frame))

	// Send message and wait for response with timeout
	start := time.Now()
	responseData, err := serialhelper.SerialSendReceive(3, gpio.High, gpio.Low, time.Second, []byte(frame), baudRate)
	if err != nil {
		return nil, err
	}
	log.Printf("Response time: %s", time.Since(start))
	log.Println("Response:", string(responseData))

	// Strip surrounding < >
	if len(responseData) < 2 || responseData[0] != '<' || responseData[len(responseData)-1] != '>' {
		return nil, fmt.Errorf("invalid response frame: %q", responseData)
	}
	inner := responseData[1 : len(responseData)-1]

	// Check that response has correct number of parts
	parts := bytes.Split(inner, []byte("|"))
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid response format, got %d parts from '%s", len(parts), string(inner))
	}

	// Verify checksum
	receivedChecksum, err := strconv.Atoi(string(parts[1]))
	if err != nil {
		return nil, fmt.Errorf("invalid checksum format '%s': %w", string(parts[1]), err)
	}
	if computeChecksum(parts[0]) != receivedChecksum {
		return nil, fmt.Errorf("checksum mismatch, got %d, expected %d", receivedChecksum, computeChecksum(parts[0]))
	}

	// Unmarshal response
	response := &uartMessage{}
	err = json.Unmarshal(parts[0], &response)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response '%s': %w", string(parts[0]), err)
	}

	return response, nil
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

	var msg uartMessage

	switch {
	case args.Command != nil:
		data, err := json.Marshal(map[string]string{"command": args.Command.Command})
		if err != nil {
			return err
		}
		msg = uartMessage{Type: "command", Data: string(data)}

	case args.Read != nil:
		data, err := json.Marshal(map[string]string{"var": args.Read.Variable})
		if err != nil {
			return err
		}
		msg = uartMessage{Type: "read", Data: string(data)}

	case args.Write != nil:
		data, err := json.Marshal(map[string]string{"var": args.Write.Variable, "val": args.Write.Value})
		if err != nil {
			return err
		}
		msg = uartMessage{Type: "write", Data: string(data)}

	default:
		return fmt.Errorf("no subcommand given")
	}

	response, err := sendMessage(msg, args.BaudRate)
	if err != nil {
		return err
	}

	if response.Type == "NACK" {
		return fmt.Errorf("NACK response: %s", response.Data)
	}
	fmt.Printf("Response: type=%s data=%s\n", response.Type, response.Data)
	return nil
}

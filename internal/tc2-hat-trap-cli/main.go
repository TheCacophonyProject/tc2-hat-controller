package trapcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	BaudRate int         `arg:"--baud-rate" help:"Baud rate for UART communication."`
	goconfig.ConfigArgs
	logging.LogArgs
}

type CMDMessage struct {
	ID      int    `arg:"--id,required" help:"The ID of the message to send."`
	Type    string `arg:"--type,required" help:"The type of message to send."`
	Payload string `arg:"--payload,required" help:"The payload of the message to send."`
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
	fmt.Printf("type=%s payload=%s\n", response.Type, response.Payload)
	return nil
}

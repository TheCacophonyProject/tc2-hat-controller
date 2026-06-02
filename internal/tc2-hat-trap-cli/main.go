package trapcli

import (
	"errors"
	"fmt"
	"os"

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

type ReadTime struct{}

type WriteTime struct {
	Time string `arg:"--time" help:"The time to write."`
}

type Listen struct{}

type Restart struct{}

var defaultArgs = Args{
	BaudRate: 9600,
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

	if args.Listen != nil {
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
	}

	messenger := comms.NewTrapMessenger(port)
	messenger.Start()

	switch {
	case args.Message != nil:
		message := comms.Message{Type: args.Message.Type, Payload: args.Message.Payload}
		return comms.HandleResponse(messenger.SendMessage(message))

	case args.Restart != nil:
		return messenger.Restart()

	case args.CopyFile != nil:
		fileUpdated, err := messenger.CopyFile(args.CopyFile.Source, args.CopyFile.Dest, args.CopyFile.Force)
		if err != nil {
			return err
		}
		if fileUpdated {
			log.Info("File updated.")
		} else {
			log.Info("File is already up to date.")
		}
		return messenger.CommitFiles()

	case args.CopyDir != nil:
		fileUpdated, err := messenger.CopyDir(args.CopyDir.Source, args.CopyDir.Dest, args.CopyDir.Force)
		if err != nil {
			return err
		}
		if fileUpdated {
			log.Info("Files updated.")
		} else {
			log.Info("Files are already up to date.")
		}
		return nil

	case args.ReadTime != nil:
		return messenger.ReadTime()

	case args.WriteTime != nil:
		return messenger.WriteTime(args.WriteTime.Time)

	default:
		return fmt.Errorf("no subcommand given")
	}
}

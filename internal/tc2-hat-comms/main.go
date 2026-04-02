package comms

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/alexflint/go-arg"
	"github.com/google/go-cmp/cmp"
	"github.com/rjeczalik/notify"
	"periph.io/x/conn/v3/gpio"
)

var (
	version = "<not set>"
	log     = logging.NewLogger("info")
)

type Args struct {
	SendTestClassification *TestClassification `arg:"subcommand:send-test-classification" help:"Send a test classification."`
	Baud                   int                 `arg:"--baud" help:"The serial baud rate (this will be removed and put in the config in the future). If using at-esl baud rate of 4800 will be forced." default:"115200"`
	goconfig.ConfigArgs
	logging.LogArgs
}

type TestClassification struct {
	Animal     string `arg:"--animal" help:"The animal to send a test classification for."`
	Confidence int32  `arg:"--confidence" help:"The confidence level to send a test classification for."`
}

// checkConfigChanges will compare the config from when first loaded to a new config each time
// the config file is modified.
// If there is a difference then the program will exit and systemd will restart the service, causing
// the new config to be loaded.
func checkConfigChanges(conf *CommsConfig, configDir string) error {
	configFilePath := filepath.Join(configDir, goconfig.ConfigFileName)
	fsEvents := make(chan notify.EventInfo, 1)
	if err := notify.Watch(configFilePath, fsEvents, notify.InCloseWrite, notify.InMovedTo); err != nil {
		return err
	}
	defer notify.Stop(fsEvents)

	for {
		<-fsEvents
		newConfig, err := ParseCommsConfig(configDir)
		log.Debug("New config:", newConfig)

		if err != nil {
			log.Error("error reloading config:", err)
			continue
		}
		diff := cmp.Diff(conf, newConfig)
		log.Debug("Config diff:", diff)
		if diff != "" {
			log.Info("Config changed. Exiting to allow systemctl to restart service.")
			os.Exit(0)
		} else {
			log.Info("No relevant changes detected in config file.")
		}
	}
}

var defaultArgs = Args{}

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

	config, err := ParseCommsConfig(args.ConfigDir)
	if err != nil {
		return err
	}
	config.BaudRate = args.Baud

	go checkConfigChanges(config, args.ConfigDir)

	if !config.Enable {
		log.Info("Comms disabled, not doing anything.")
		for {
			time.Sleep(time.Second)
		}
	}

	log.Info("Species to trap:\n", tracks.Species(config.TrapSpecies))
	log.Info("Species to protect:\n", tracks.Species(config.ProtectSpecies))

	eventsChan := make(chan event, 10)

	// Every comms channel receives battery events
	err = addBatteryEvents(eventsChan)
	if err != nil {
		return err
	}

	switch config.CommsOut {
	case "uart", "json-out":
		log.Info("Running UART/json-out.")

		// uart comms channel listens for tracking events
		err = addTrackingEvents(eventsChan)
		if err != nil {
			return err
		}

		port, err := serialhelper.OpenSerial(gpio.High, gpio.Low, config.BaudRate)
		if err != nil {
			return fmt.Errorf("failed to open serial port: %v", err)
		}
		defer port.Close()

		messenger := NewUartMessenger(port)
		messenger.Start()

		if err := processUart(config, args.SendTestClassification, eventsChan, messenger); err != nil {
			return err
		}
	case "simple":
		log.Info("Running simple output.")

		// simple comms channel listens for tracking events
		err = addTrackingEvents(eventsChan)
		if err != nil {
			return err
		}

		if err := processSimpleOutput(config, eventsChan); err != nil {
			return err
		}
	case "trap-control":
		log.Info("Running trap-control output.")

		// TODO, check what speed we want for this
		config.BaudRate = 9600

		// Add tracking events to the channel
		err = addTrackingEvents(eventsChan)
		if err != nil {
			return err
		}

		// Add recording start/stop events to the channel
		err = addRecordingEvents(eventsChan)
		if err != nil {
			return err
		}

		port, err := serialhelper.OpenSerial(gpio.High, gpio.Low, config.BaudRate)
		if err != nil {
			return fmt.Errorf("failed to open serial port: %v", err)
		}
		defer port.Close()

		messenger := NewUartMessenger(port)
		messenger.Start()

		// Run the trap control process
		if err := processTrapControl(config, eventsChan, messenger); err != nil {
			return err
		}
	case "at-esl":
		log.Info("Running AT-ESL output.")
		config.BaudRate = 4800 // Force AT-ESL baud rate to be 4800

		// at-esl comms channel listens for tracking *reprocessed* events
		err = addTrackingReprocessedEvents(eventsChan)
		if err != nil {
			return err
		}

		if err := processATESL(config, args.SendTestClassification, eventsChan); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown output type '%s'", config.CommsOut)
	}

	return nil
}

package comms

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/alexflint/go-arg"
	"github.com/google/go-cmp/cmp"
	"github.com/rjeczalik/notify"
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

	if config.CommsOut == "uart" && config.Bluetooth {
		log.Error("Can't have output set to UART and Bluetooth enabled at the same time.")
		return fmt.Errorf("can't have output set to UART and Bluetooth enabled at the same time")
	}

	log.Info("Species to trap:\n", tracks.Species(config.TrapSpecies))
	log.Info("Species to protect:\n", tracks.Species(config.ProtectSpecies))

	eventsChan := make(chan event, 10)

	// Every comms channel receives battery events
	err = addBatteryEvents(eventsChan)
	if err != nil {
		return err
	}

	// Every comms channel tracking reprocessed events
	err = addTrackingReprocessedEvents(eventsChan)
	if err != nil {
		return err
	}

	switch config.CommsOut {
	case "uart":
		log.Info("Running UART output.")
		if err := processUart(config, args.SendTestClassification, eventsChan); err != nil {
			return err
		}
	case "simple":
		log.Info("Running simple output.")
		if err := processSimpleOutput(config, eventsChan); err != nil {
			return err
		}
	case "at-esl":
		log.Info("Running AT-ESL output.")
		config.BaudRate = 4800 // Force AT-ESL baud rate to be 4800
		if err := processATESL(config, args.SendTestClassification, eventsChan); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown output type '%s'", config.CommsOut)
	}

	return nil

	/*

		trapActiveUntil := time.Time{}
		trapActive := false

		// Initialize the periph host drivers
		if _, err := host.Init(); err != nil {
			log.Printf("Failed to initialize periph: %v\n", err)
			return err
		}

		log.Info("Get lock on serial port")
		if config.CommsOut == "uart" || config.CommsOut == "simple" {
			serialFile, err := serialhelper.GetSerial(3, gpio.High, gpio.Low, time.Second)
			if err != nil {
				return err
			}
			defer serialhelper.ReleaseSerial(serialFile)
		}
		log.Info("Done")

		protectDuration := time.Minute
		trapDuration := time.Duration(args.TrapStayActiveDuration) * time.Second

		var newTrack *trackingEvent
		lastProtectSpeciesSighting := time.Time{}
		lastTrapSpeciesSighting := time.Time{}

		for {

			now := time.Now()
			newTrapActive :=
				(lastProtectSpeciesSighting.Add(protectDuration).Before(now) && // Nothing to protect has been seen recently.
					lastTrapSpeciesSighting.Add(trapDuration).After(now)) // And something to trap has been sighted recently.

			if trapActive != newTrapActive {
				trapActive = newTrapActive

				if trapActive {
					log.Println("Activating trap")
				} else {
					log.Println("Deactivating trap")
				}

				switch args.OutputType {
				case "uart":
					log.Info("Outputting trap active state via UART")
					if err := processUart(); err != nil {
						return err
					}
					// TODO

				case "bluetooth":
					log.Info("Outputting trap active state via bluetooth")
					if err := processBluetooth(); err != nil {
						return err
					}
					// TODO

				case "digital":
					log.Info("Outputting trap active state via digital signals")
					//if err := processDigital(); err != nil {
					//	return err
					//}

				default:
					return fmt.Errorf("unhandled output type: %s", args.OutputType)
				}
			}

			var delay = 10 * time.Second
			if trapActive && time.Until(trapActiveUntil) < delay {
				delay = time.Until(trapActiveUntil)
			}

			newTrack = nil
			log.Debug("Waiting ")
			select {
			case t := <-trackingSignals:
				newTrack = &t
				log.Debugf("Found new track: %+v", newTrack)

				if newTrack.species.MatchSpeciesWithConfidence(protectSpecies) {
					log.Debug("Found an animal that needs to be protected, deactivating trap")
					lastProtectSpeciesSighting = time.Now()
					//trapActiveUntil = time.Time{}
				} else if newTrack.species.MatchSpeciesWithConfidence(trapSpecies) {
					log.Debug("Found an animal that needs to be trapped, activating trap")
					lastTrapSpeciesSighting = time.Now()

					//trapActiveUntil = time.Now().Add(time.Duration(args.TrapStayActiveDuration) * time.Second)
				} else {
					log.Debug("No animals need to be protected or trapped, not changing trap state.")
				}

			case <-time.After(delay):
				log.Debug("Scheduled check")
			}
		}
	*/

	/*
		for t := range tracks {
			log.Infof("Found track: %+v", t)
		}

		// Start dbus to listen for classification messages.

		if err := beep(); err != nil {
			return err
		}

		log.Println("Starting UART service")
		if err := startService(); err != nil {
			return err
		}

		trapActive = false
		if err := sendTrapActiveState(trapActive); err != nil {
			return err
		}

		for {
			waitUntil := time.Now().Add(5 * time.Second)
			if trapActive {
				waitUntil = activateTrapUntil
			}

			select {
			case <-activeTrapSig:
			case <-time.After(time.Until(waitUntil)):
			}
			trapActive = time.Now().Before(activateTrapUntil)

			if err := sendTrapActiveState(trapActive); err != nil {
				return err
			}
		}
	*/
}

/*
func checkClassification(data map[byte]byte) error {
	for k, v := range data {
		if k == 1 && v > 80 {
			activateTrap()
		}
		if k == 7 && v > 80 {
			activateTrap()
		}
	}
	return nil
}

func activateTrap() {
	log.Println("Activating trap")
	activateTrapUntil = time.Now().Add(time.Minute)
	activeTrapSig <- "trap"
}
*/

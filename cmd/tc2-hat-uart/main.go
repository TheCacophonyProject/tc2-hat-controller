package main

import (
	"fmt"
	"strings"
	"time"

	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/alexflint/go-arg"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/host/v3"
)

var (
	version = "<not set>"
	//activateTrapUntil = time.Now()
	//activeTrapSig     = make(chan string)
	log = logging.NewLogger("info")
)

var outputTypes = []string{"uart", "bluetooth", "digital"}

type Args struct {
	OutputType             string `arg:"--output-type,required" help:"Output type (uart, bluetooth, digital)"`
	PowerOutPlugin         bool   `arg:"--power-output-plug" help:"If the output plug should be powered" default:"false"`
	TrapSpeciesFile        string `arg:"--target" help:"File containing trap species" default:"/etc/cacophony/trap-species.json"`
	ProtectSpecies         string `arg:"--protect" help:"File containing protect species" default:"/etc/cacophony/protect-species.json"`
	TrapStayActiveDuration int    `arg:"--trap-stay-active-duration" help:"The number of seconds the trap should stay active for" default:"30"`

	goconfig.ConfigArgs
	logging.LogArgs
}

func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{}

	arg.MustParse(&args)

	args.OutputType = strings.ToLower(args.OutputType)
	validOutputType := false
	for _, t := range outputTypes {
		if args.OutputType == t {
			validOutputType = true
			break
		}
	}
	if !validOutputType {
		log.Fatalf("Invalid output type '%s'. Should be one of '%s'.", args.OutputType, strings.Join(outputTypes, "', '"))
	}

	return args
}

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err)
	}
}

type Read struct {
	Var string `json:"var,omitempty"`
}

type ReadResponse struct {
	Val string `json:"var,omitempty"`
}

func runMain() error {
	args := procArgs()

	log = logging.NewLogger(args.LogLevel)

	log.Printf("Running version: %s", version)

	config, err := ParseCommsConfig(args.ConfigDir)
	if err != nil {
		return err
	}

	log.Info("Loading trap and protect species")
	trapSpecies, err := tracks.LoadSpeciesFromFile(args.TrapSpeciesFile)
	if err != nil {
		return err
	}
	protectSpecies, err := tracks.LoadSpeciesFromFile(args.ProtectSpecies)
	if err != nil {
		return err
	}
	log.Info("Species to trap:\n", trapSpecies)
	log.Info("Species to protect:\n", protectSpecies)

	trackingSignals, err := getTrackingSignals()
	if err != nil {
		return err
	}

	switch args.OutputType {
	case "bluetooth":
		if err := processBluetooth(); err != nil {
			return err
		}
	case "uart":
		if err := processUart(); err != nil {
			return err
		}
	case "digital":
		if err := processDigital(config, trackingSignals); err != nil {
			return err
		}
	}

	trapActiveUntil := time.Time{}
	trapActive := false

	// Initialize the periph host drivers
	if _, err := host.Init(); err != nil {
		log.Printf("Failed to initialize periph: %v\n", err)
		return err
	}

	log.Info("Get lock on serial port")
	if args.OutputType == "uart" || args.OutputType == "digital" {
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

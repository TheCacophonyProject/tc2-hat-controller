package main

import (
	"fmt"
	"time"

	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/alexflint/go-arg"
)

var (
	version = "<not set>"
	log     = logging.NewLogger("info")
)

type Args struct {
	goconfig.ConfigArgs
	logging.LogArgs
}

func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{}
	arg.MustParse(&args)
	return args
}

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err)
	}
}

func runMain() error {
	args := procArgs()

	log = logging.NewLogger(args.LogLevel)

	log.Printf("Running version: %s", version)

	config, err := ParseCommsConfig(args.ConfigDir)
	if err != nil {
		return err
	}

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

	trackingSignals, err := getTrackingSignals()
	if err != nil {
		return err
	}

	switch config.CommsOut {
	case "uart":
		if err := processUart(); err != nil {
			return err
		}
	case "simple":
		if err := processSimpleOutput(config, trackingSignals); err != nil {
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

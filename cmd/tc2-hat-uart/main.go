package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/TheCacophonyProject/go-utils/logging"
	serialhelper "github.com/TheCacophonyProject/tc2-hat-controller"
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/alexflint/go-arg"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
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
	OutputType      string `arg:"--output-type,required" help:"Output type (uart, bluetooth, digital)"`
	PowerOutPlugin  bool   `arg:"--power-output-plug" help:"If the output plug should be powered" default:"false"`
	TrapSpeciesFile string `arg:"--target" help:"File containing trap species" default:"/etc/cacophony/trap-species.json"`
	ProtectSpecies  string `arg:"--protect" help:"File containing protect species" default:"/etc/cacophony/protect-species.json"`

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

// UartMessage represents the data structure for communication with a device connected on UART.
// - ID: Identifier of the message being sent or the message being responded to.
// - Response: Indicates if the message is a response.
// - Type: Specifies the type of message (e.g., write, read, command, ACK, NACK).
// - Data: Contains the actual data payload, which varies depending on the type or response.
type UartMessage struct {
	ID       int    `json:"id,omitempty"`
	Response bool   `json:"response,omitempty"`
	Type     string `json:"type,omitempty"`
	Data     string `json:"data,omitempty"`
}

type Command struct {
	Command string `json:"command"`
	Args    string `json:"args,omitempty"`
}

type Write struct {
	Var string      `json:"var,omitempty"`
	Val interface{} `json:"val,omitempty"`
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

	tracks, err := getTrackingSignals()
	if err != nil {
		return err
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

	for {
		newTrapActive := trapActiveUntil.After(time.Now())

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
				// TODO

			case "bluetooth":
				log.Info("Outputting trap active state via bluetooth")
				// TODO

			case "digital":
				log.Info("Outputting trap active state via digital signals")
				// Get a handle for GPIO 14 (BCM 14, physical pin 8 on the Raspberry Pi)
				pin := gpioreg.ByName("GPIO14")
				if pin == nil {
					err := fmt.Errorf("failed to find GPIO14")
					return err
				}

				if trapActive {
					log.Info("Driving pin high")
					err := pin.Out(gpio.High)
					if err != nil {
						return err
					}
				} else {
					log.Info("Driving pin low")
					err := pin.Out(gpio.Low)
					if err != nil {
						return err
					}
				}

			default:
				return fmt.Errorf("unhandled output type: %s", args.OutputType)
			}
		}

		var delay = 10 * time.Second
		if trapActive && time.Until(trapActiveUntil) < delay {
			delay = time.Until(trapActiveUntil)
		}

		log.Debug("Waiting ")
		select {
		case t := <-tracks:
			log.Debugf("Found new track: %+v", t)
			if t.animal == "possum" && t.confidence > 50 {
				trapActiveUntil = time.Now().Add(33 * time.Second)
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

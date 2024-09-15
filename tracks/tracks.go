package tracks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/godbus/dbus/v5"
)

type Species map[string]int32

// The trap will trigger if a classification is one of the trap species and is equal or above the
// provided confidence level and there is no classification that is in one of the protect species that
// is equal or above the provided confidence level.

// So a classification of (possum: 80, bird: 40) will trigger the trap.
// A classification of (possum: 80, kiwi: 40) will not trigger the trap.

// shouldTrigger will return if the trap should trigger based on the classifications, trapSpecies, and protectSpecies.
func (s Species) ShouldTrigger(trapSpecies, protectSpecies Species) bool {
	trigger := false
	for animal, conf := range s {
		requiredConf, ok := trapSpecies[animal]
		if ok && conf >= requiredConf {
			trigger = true
			break
		}
	}

	if !trigger {
		return false
	}

	for animal, conf := range s {
		requiredConf, ok := protectSpecies[animal]
		if ok && conf >= requiredConf {
			return false
		}
	}

	return true
}

func (c Species) String() string {
	outLines := []string{}
	for k, v := range c {
		outLines = append(outLines, fmt.Sprintf("Animal: '%s', Confidence: %d", k, v))
	}
	return strings.Join(outLines, "\n")
}

type Track struct {
	Species
	Animals     string
	Confidence  int32
	BoundingBox [4]int32
	Motion      bool
}

func GetTrackingSignals(log *logging.Logger) (chan Track, error) {
	if log == nil {
		log = logging.NewLogger("info")
	}

	// Connect to the system bus
	conn, err := dbus.SystemBus()
	if err != nil {
		log.Fatalf("Failed to connect to system bus: %v", err)
	}

	// Add a match rule to listen for our dbus signals
	rule := "type='signal',interface='org.cacophony.thermalrecorder'"
	call := conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, rule)
	if call.Err != nil {
		log.Fatalf("Failed to add match rule: %v", call.Err)
	}

	// Create a channel to receive signals
	c := make(chan *dbus.Signal, 10)
	conn.Signal(c)

	// Create a channel to send tracking events
	tracks := make(chan Track, 10)

	// Listen for signals
	log.Println("Listening for D-Bus signals from org.cacophony.thermalrecorder...")

	// Listen for signals, process and send tracking events to the channel.
	go func() {
		for signal := range c {
			if signal.Name == "org.cacophony.thermalrecorder.Tracking" {
				log.Debug("Received tracking event:")
				if len(signal.Body) != 4 {
					log.Errorf("Unexpected signal format in body: %v", signal.Body)
					continue
				}
				if len(signal.Body[2].([]int32)) != 4 {
					log.Errorf("Unexpected signal format in bounding box: %v", signal.Body[2])
					continue
				}

				log.Debugf("Animal: %v", signal.Body[0])
				log.Debugf("Confidence: %v", signal.Body[1])
				log.Debugf("Bounding box: %v", signal.Body[2])
				log.Debugf("Motion detected: %v", signal.Body[3])

				var boundingBox [4]int32
				copy(boundingBox[:], signal.Body[2].([]int32))
				t := Track{
					Species:     Species{signal.Body[0].(string): signal.Body[1].(int32)},
					BoundingBox: boundingBox,
					Motion:      signal.Body[3].(bool),
				}
				tracks <- t
			}
		}
	}()

	return tracks, nil
}

// LoadSpeciesFromFile loads species confidence levels from a JSON file
func LoadSpeciesFromFile(filePath string) (Species, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	// Read the file content
	byteValue, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %v", err)
	}

	// Unmarshal JSON data into the Species map
	var species Species
	if err := json.Unmarshal(byteValue, &species); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON: %v", err)
	}

	return species, nil
}

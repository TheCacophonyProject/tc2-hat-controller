// This section is for listening to broadcasts events over DBus.

package main

import (
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/godbus/dbus/v5"
)

type trackingEvent struct {
	Species             tracks.Species
	What                string
	Confidence          int32
	Region              [4]int32
	Frame               int32
	Mass                int32
	BlankRegion         bool
	Tracking            bool
	LastPredictionFrame int32
}

var animalsList = []string{"bird", "cat", "deer", "dog", "false-positive", "hedgehog", "human", "kiwi", "leporidae", "mustelid", "penguin", "possum", "rodent", "sheep", "vehicle", "wallaby", "land-bird"}

func getTrackingSignals() (chan trackingEvent, error) {
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
	tracksChan := make(chan trackingEvent, 10)

	// Listen for signals
	log.Println("Listening for D-Bus signals from org.cacophony.thermalrecorder...")

	// Listen for signals, process and send tracking events to the channel.
	go func() {
		for signal := range c {
			if signal.Name == "org.cacophony.thermalrecorder.Tracking" {
				log.Debug("Received tracking event:")
				if len(signal.Body) != 9 {
					log.Errorf("Unexpected signal format in body: %v", signal.Body)
					continue
				}

				log.Debugf("Scores: %v", signal.Body[0])
				log.Debugf("What: %v", signal.Body[1])
				log.Debugf("Confidences: %v", signal.Body[2])
				log.Debugf("Region: %v", signal.Body[3])
				log.Debugf("Frame: %v", signal.Body[4])
				log.Debugf("Mass: %v", signal.Body[5])
				log.Debugf("Blank region: %v", signal.Body[6])
				log.Debugf("Tracking: %v", signal.Body[7])
				log.Debugf("Last prediction frame: %v", signal.Body[8])

				var region [4]int32
				copy(region[:], signal.Body[3].([]int32))

				species := tracks.Species{}
				for i, v := range animalsList {
					species[v] = signal.Body[0].([]int32)[i]
				}

				t := trackingEvent{
					Species:             species,
					What:                signal.Body[1].(string),
					Confidence:          signal.Body[2].(int32),
					Region:              region,
					Frame:               signal.Body[4].(int32),
					Mass:                signal.Body[5].(int32),
					BlankRegion:         signal.Body[6].(bool),
					Tracking:            signal.Body[7].(bool),
					LastPredictionFrame: signal.Body[8].(int32),
				}

				log.Debugf("Sending tracking event: %+v", t)

				tracksChan <- t
			}
		}
	}()

	return tracksChan, nil
}

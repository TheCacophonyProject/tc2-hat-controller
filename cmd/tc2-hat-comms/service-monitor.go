// This section is for listening to broadcasts events over DBus.

package main

import (
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/godbus/dbus/v5"
)

type trackingEvent struct {
	species     tracks.Species
	boundingBox [4]int32
	motion      bool
}

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
				t := trackingEvent{
					species: tracks.Species{
						signal.Body[0].(string): signal.Body[1].(int32),
					},
					boundingBox: boundingBox,
					motion:      signal.Body[3].(bool),
				}
				tracksChan <- t
			}
		}
	}()

	return tracksChan, nil
}

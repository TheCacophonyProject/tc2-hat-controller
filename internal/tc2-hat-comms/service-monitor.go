// This section is for listening to broadcasts events over DBus.

package comms

import (
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/godbus/dbus/v5"
)

type event interface {
	isEvent()
}

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

func (t trackingEvent) isEvent() {}

type batteryEvent struct {
	event
	Voltage float64
	Percent float64
}

var animalsList = []string{"bird", "cat", "deer", "dog", "false-positive", "hedgehog", "human", "kiwi", "leporidae", "mustelid", "penguin", "possum", "rodent", "sheep", "vehicle", "wallaby", "land-bird"}
var fpModelLabels = []string{"false-positive", "animal"}

func addTrackingEvents(eventsChan chan event) error {
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

	// Listen for signals
	log.Println("Listening for D-Bus signals from org.cacophony.thermalrecorder...")

	// Listen for signals, process and send tracking events to the channel.
	go func() {
		for signal := range c {
			if signal.Name == "org.cacophony.thermalrecorder.Tracking" {
				log.Debug("Received tracking event:")
				if len(signal.Body) != 12 {
					log.Errorf("Unexpected signal format in body: %v", signal.Body)
					continue
				}
				log.Debugf("ClipId: %v", signal.Body[0])
				log.Debugf("TrackId: %v", signal.Body[1])
				log.Debugf("Scores: %v", signal.Body[2])
				log.Debugf("What: %v", signal.Body[3])
				log.Debugf("Confidences: %v", signal.Body[4])
				log.Debugf("Region: %v", signal.Body[5])
				log.Debugf("Frame: %v", signal.Body[6])
				log.Debugf("Mass: %v", signal.Body[7])
				log.Debugf("Blank region: %v", signal.Body[8])
				log.Debugf("Tracking: %v", signal.Body[9])
				log.Debugf("Last prediction frame: %v", signal.Body[10])
				log.Debugf("Model Id: %v", signal.Body[11])

				var region [4]int32
				copy(region[:], signal.Body[5].([]int32))

				species := tracks.Species{}
				if len(signal.Body[2].([]int32)) == 2 {
					for i, v := range fpModelLabels {
						species[v] = signal.Body[2].([]int32)[i]
					}
				} else {
					for i, v := range animalsList {
						if len(signal.Body[2].([]int32)) < i {
							species[v] = signal.Body[2].([]int32)[i]
						} else {
							log.Warnf("Warning possible overrun accessing element %d of Scores: %v", i, signal.Body[2])
						}
					}
				}

				t := trackingEvent{
					Species:             species,
					What:                signal.Body[3].(string),
					Confidence:          signal.Body[4].(int32),
					Region:              region,
					Frame:               signal.Body[6].(int32),
					Mass:                signal.Body[7].(int32),
					BlankRegion:         signal.Body[8].(bool),
					Tracking:            signal.Body[9].(bool),
					LastPredictionFrame: signal.Body[10].(int32),
				}

				log.Debugf("Sending tracking event: %+v", t)

				eventsChan <- t
			}
		}
	}()

	return nil
}

func addBatteryEvents(eventsChan chan event) error {
	// Connect to the system bus
	conn, err := dbus.SystemBus()
	if err != nil {
		log.Fatalf("Failed to connect to system bus: %v", err)
	}

	// Add a match rule to listen for our dbus signals
	rule := "type='signal',interface='org.cacophony.attiny'"
	call := conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, rule)
	if call.Err != nil {
		log.Fatalf("Failed to add match rule: %v", call.Err)
	}

	// Create a channel to receive signals
	c := make(chan *dbus.Signal, 10)
	conn.Signal(c)

	// Listen for signals
	log.Println("Listening for D-Bus signals from org.cacophony.attiny...")

	// Listen for signals, process and send tracking events to the channel.
	go func() {
		for signal := range c {
			if signal.Name == "org.cacophony.attiny.Battery" {
				log.Debug("Received battery event.")
				if len(signal.Body) != 2 {
					log.Errorf("Unexpected signal format in body: %v", signal.Body)
					continue
				}

				log.Debugf("Voltage: %v", signal.Body[0])
				log.Debugf("Percent: %v", signal.Body[1])

				t := batteryEvent{
					Voltage: signal.Body[0].(float64),
					Percent: signal.Body[1].(float64),
				}

				log.Debugf("Sending battery event: %+v", t)

				eventsChan <- t
			}
		}
	}()

	return nil
}

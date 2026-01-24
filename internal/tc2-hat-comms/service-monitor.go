// This section is for listening to broadcasts events over DBus.

package comms

import (
	"time"
	"strconv"
	"strings"

	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/godbus/dbus/v5"
)

type models struct {
	Id	   int32
	Labels []string
}

var (
	animalsList	  = models{Id: 1, Labels: []string{"bird","cat","deer","dog","false-positive","hedgehog","human","kiwi","leporidae","mustelid","penguin","possum","rodent","sheep","vehicle","wallaby"}}
	fpModelLabels = models{Id: 1004, Labels: []string{"animal","false-positive"}}
)

type event interface {
	isEvent()
}

type trackingEvent struct {
	Species             tracks.Species
	What                string
	Confidence          int32
	Region              [4]int32
	ClipId              int32
	TrackId             int32
	Frame               int32
	Mass                int32
	BlankRegion         bool
	Tracking            bool
	LastPredictionFrame int32
	ClipAgeSeconds		int32
}

func (t trackingEvent) isEvent() {}

type batteryEvent struct {
	event
	Voltage float64
	Percent float64
}

func addTrackingReprocessedEvents(eventsChan chan event) error {
    targetSignalName := "org.cacophony.thermalrecorder.TrackingReprocessed"
    return addTrackingEventsForSignal(eventsChan, targetSignalName)
}

func addTrackingEvents(eventsChan chan event) error {
	targetSignalName := "org.cacophony.thermalrecorder.Tracking"
	return addTrackingEventsForSignal(eventsChan, targetSignalName)
}

func addTrackingEventsForSignal(eventsChan chan event, targetSignalName string) error {

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
	log.Infof("Listening for D-Bus signals: %s", targetSignalName)

	// Get the latest classification labels if we need them
	log.Info("Getting latest classification labels")
	getLabels()

	// Listen for signals, process and send tracking events to the channel.
	go func() {
		for signal := range c {
			if signal.Name == targetSignalName {
				log.Debugf("Received tracking event [%v]:", signal.Name)

				// Reprocessed signals have an additional parameter 'clip_end_time'
				if len(signal.Body) < 12 {
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
				if len(signal.Body) >= 13 {
					log.Debugf("Clip End Time: %v", signal.Body[12])
				}

				var modelId int32
				var modelLabels []string

				// Match the track model output to our now models
				switch modelIdType := signal.Body[11].(type) {
					case int32:
						modelId = signal.Body[11].(int32)
					// Reprocessed events have a "post-" id prefix
					case string:
						modelIdStr := strings.TrimPrefix(signal.Body[11].(string), "post-")
						val64, err := strconv.ParseInt(modelIdStr, 10, 32)
						if err != nil {
							log.Warnf("Failed to parse the model id[%v]: %v", modelIdStr, err)
							continue
						}
						modelId = int32(val64)
					default:
						log.Warnf("Model id unexpected type %v .. skipping", modelIdType)
						continue
				}

				// Get the labels for the model used in the prediction
				switch modelId {
					case fpModelLabels.Id: 
						modelLabels = fpModelLabels.Labels
					case animalsList.Id:
						modelLabels = animalsList.Labels
					default:
						log.Warnf("Model id key not known %v [%v, %v]", modelId, fpModelLabels.Id, animalsList.Id)
						continue
				}			

				// Loop through our track species and get the model scores 
				species := tracks.Species{}
				for i, v := range modelLabels {
					species[v] = signal.Body[2].([]int32)[i]
				}

				// Get the region details
				var region [4]int32
				copy(region[:], signal.Body[5].([]int32))

				// See if we have a clip end time
				clipAgeSeconds := int32(0)
				if len(signal.Body) >= 13 {
					ts := signal.Body[12].(float64);
					now := time.Now()
					target := time.Unix(int64(ts), int64((ts-float64(int64(ts)))*1e9),)

					clipAgeSeconds = int32(now.Sub(target).Seconds());
					log.Debugf("Clip is %d seconds old", clipAgeSeconds)
				}

				// Finally let's build our tracking event
				t := trackingEvent{
					Species:             species,
					ClipId:              signal.Body[0].(int32),
					TrackId:             signal.Body[1].(int32),
					What:                signal.Body[3].(string),
					Confidence:          signal.Body[4].(int32),
					Region:              region,
					Frame:               signal.Body[6].(int32),
					Mass:                signal.Body[7].(int32),
					BlankRegion:         signal.Body[8].(bool),
					Tracking:            signal.Body[9].(bool),
					LastPredictionFrame: signal.Body[10].(int32),
					ClipAgeSeconds:      clipAgeSeconds,
				}
				log.Debugf("Sending tracking event: %+v", t)

				eventsChan <- t
			}
		}
	}()

	return nil
}

func addBatteryEvents(eventsChan chan event) error {
	// Listen for signals
	targetSignalName := "org.cacophony.attiny.Battery"

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

	log.Infof("Listening for D-Bus signals: %s", targetSignalName)
	// Listen for signals, process and send tracking events to the channel.
	go func() {
		for signal := range c {
			if signal.Name == targetSignalName {
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

func getLabels() {
	// Connect to the system bus
	conn, err := dbus.SystemBus()
	if err != nil {
		log.Fatalf("Failed to connect to system bus: %v", err)
	}

	// Try and get the classification labels
	connObj := conn.Object("org.cacophony.thermalrecorder", "/org/cacophony/thermalrecorder")
	t_call := connObj.Call("org.cacophony.thermalrecorder.ClassificationLabels", 0)
	if t_call.Err != nil {
		log.Warnf("Failed to get classification labels, will use defaults: animalsList: %+v, fpModelLabels: %+v", animalsList, fpModelLabels)
		return
	}
	bodyMap := t_call.Body[0].(map[int32][]string)

	// Out model labels have id '1' .. false-postitives are the other element.
	// e.g. [map[1:[bird cat deer ... vehicle wallaby] 1004:[animal false-positive]]]
	for k, v := range bodyMap {
		if k == animalsList.Id {
			animalsList.Labels = v
		} else if k == fpModelLabels.Id {
			fpModelLabels.Labels = v
		} else {
			log.Warnf("Unexpected classification label id: %v, with labels: %v", k, bodyMap)
		}
	}
	log.Infof("Classification labels updated: animalsList: %+v, fpModelLabels: %+v", animalsList, fpModelLabels)
}

func getThumbnail(clip_id int32, track_id int32) [][]uint16 {
	// Connect to the system bus
	conn, err := dbus.SystemBus()
	if err != nil {
		log.Fatalf("Failed to connect to system bus: %v", err)
	}

	// Try and get the associated thumbnail
	connObj := conn.Object("org.cacophony.thermalrecorder", "/org/cacophony/thermalrecorder")
	t_call := connObj.Call("org.cacophony.thermalrecorder.GetThumbnail", 0, clip_id, track_id)
	if t_call.Err != nil {
		log.Warnf("Failed to get thumbnail (clip id: %d, track_id: %d): %v", clip_id, track_id, t_call.Err)
		return nil
	}

	switch frame := t_call.Body[0].(type) {
		case [][]uint16:
		    // Access row/col
		    log.Debugf("Thubnail (clip id: %d, track_id: %d) is: %d×%d", clip_id, track_id, len(frame), len(frame[0]))
			return t_call.Body[0].([][]uint16)
		default:
		    log.Warnf("GetThumbnail returned an unexpected 2D type: %T", frame)
	}
	return nil
}

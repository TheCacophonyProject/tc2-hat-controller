// Output mode: sends events out over serial in JSON format.

package comms

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
)

type ClassificationData struct {
	Species    tracks.Species `json:"species"`
	Confidence int32          `json:"confidence"`
}

type jsonOut struct {
	Type string             `json:"type,omitempty"`
	Data ClassificationData `json:"data"`
}

func processJSONOut(config *CommsConfig, testClassification *TestClassification, trackingSignals chan event, port *serialhelper.SerialPort) error {
	if testClassification != nil {
		log.Println("Sending a test classification over UART")

		species := tracks.Species{
			testClassification.Animal: int32(testClassification.Confidence),
		}

		classificationData := ClassificationData{
			Species:    species,
			Confidence: int32(testClassification.Confidence),
		}

		message := jsonOut{
			Type: "classification",
			Data: classificationData,
		}
		payload, err := json.Marshal(message)
		if err != nil {
			return err
		}

		log.Printf("Sending payload: '%s'", payload)
		return port.Write(append(payload, '\r', '\n'))
	}

	for {
		log.Debug("Waiting")
		for e := range trackingSignals {
			switch v := e.(type) {
			case trackingEvent:
				fmt.Println("Tracking event:", v.Species)
				err := processTrackingEvent(v, port)
				if err != nil {
					log.Error("Error processing tracking event:", err)
				}
			default:
				log.Debug("Not processing event:", v)
				continue
			}
		}
	}
}

func processTrackingEvent(t trackingEvent, port *serialhelper.SerialPort) error {
	log.Debugf("Found new track: %+v", t)

	species := tracks.Species{}
	for k, v := range t.Species {
		if v > 0 {
			species[k] = v
		}
	}

	message := jsonOut{
		Type: "classification",
		Data: ClassificationData{
			Species:    species,
			Confidence: t.Confidence,
		},
	}

	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}

	log.Printf("Sending payload: '%s'", payload)
	start := time.Now()

	err = port.Write(append(payload, '\r', '\n'))

	log.Printf("Sent payload in %s", time.Since(start))
	return err
}

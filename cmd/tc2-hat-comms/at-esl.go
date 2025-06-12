package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"periph.io/x/conn/v3/gpio"
)

type ATESLMessenger struct {
	baudRate    int
	trapSpecies map[string]int32
}

type ATESLLastPrediction struct {
	Frame int32
	What string
}

func processATESL(config *CommsConfig, testClassification *TestClassification, eventChannel chan event) error {
	messenger := ATESLMessenger {
			config.BaudRate,
			config.TrapSpecies,
	}
	lastPrediction := ATESLLastPrediction{}

	if testClassification != nil {
		log.Println("Sending a test classification for AT ESL")
		testTrackingEvent := trackingEvent{
			Species: map[string]int32{
				testClassification.Animal: testClassification.Confidence,
			},
			What: testClassification.Animal,
			Confidence: testClassification.Confidence,
		}
		err := messenger.processTrackingEvent(testTrackingEvent, &lastPrediction)
		if err != nil {
			log.Error("Error processing test tracking event:", err)
		}
		return nil
	}

	for {
		log.Debug("Waiting")
		e := <-eventChannel

		// Process the event, depending on the type
		switch v := e.(type) {
		case trackingEvent:
			err := messenger.processTrackingEvent(v, &lastPrediction)
			if err != nil {
				log.Error("Error sending classification:", err)
			}
		case batteryEvent:
			err := messenger.processBatteryEvent(v)
			if err != nil {
				log.Error("Error sending battery reading:", err)
			}
		}
		/* TODO:
		case thumbnailEvent:
		case ...
		*/
	}
}

func (a ATESLMessenger) processBatteryEvent(b batteryEvent) error {
	log.Infof("Processing battery event: %+v", b)
	// AT command, sending a battery reading as tenths of a volt
	atCmd := fmt.Sprintf("AT+CAMBAT=%d", int32(b.Voltage*10))

	err := sendATCommand(atCmd, a.baudRate)
	if err != nil {
		log.Error("Error sending battery reading:", err)
		return err
	}
	return nil
}

func (a ATESLMessenger) processTrackingEvent(t trackingEvent, l *ATESLLastPrediction) error {

	log.Debugf("Received new tracking event What: %v, Confidence : %v, Region: %v, LastPredictionFrame: %v, Frame: %v",
                    t.What, t.Confidence, t.Region, t.LastPredictionFrame, t.Frame)

	if t.Frame == 1 {
		l.Frame = 0 // let's start again
		l.What = ""
	}

	// We can do without false-positives :)
	if t.What == "false-positive" {
		return nil
	}

	// What:possum Confidence:99 Region:[16 65 29 79] Frame:218 Mass:17 BlankRegion:false Tracking:true LastPredictionFrame:194
	// We don't really need tracks - just process predictions
	if t.Frame != t.LastPredictionFrame {
		return nil
	}

	// It's a prediction frame but within the same video stream and prediction so skip until a new stream starts
	if l.Frame > 0 && t.Frame > l.Frame && t.What == l.What {
		log.Infof("Skipping tracking prediction (frame) event: saved (%v) v event (%v) frame [%v v %v ]",
				l.Frame, t.LastPredictionFrame, l.What, t.What)
		return nil
	}

	log.Infof("Processing tracking prediction (frame) event What: %v, Confidence : %v, Region: %v, Frame: %v",
                    t.What, t.Confidence, t.Region, t.Frame)

	targetConfidence := int32(0)
	target := false
	// We've found an object - is it a target (trapable) species?
	if _, found := a.trapSpecies["any"]; found {
		target = true
		targetConfidence = a.trapSpecies["any"]
	} else if _, found := a.trapSpecies[t.What]; found {
		target = true
		targetConfidence = a.trapSpecies[t.What]
	}

	if target && t.Confidence >= targetConfidence {
		log.Infof("Track prediction of a target species with confidence: %s,%d", t.What, t.Confidence)

		atCmd := fmt.Sprintf("AT+CAM=%s,%d", t.What, t.Confidence)
		l.Frame = t.Frame // remember the frame

		err := sendATCommand(atCmd, a.baudRate)
		if err != nil {
			log.Error("Error sending classification:", err)
			return err
		}
	}

	return nil
}

func sendATWakeUp(baudRate int) error {

	log.Debugf("Wake up serial device.")
	payload := []byte("\r\rAT\r")

	retries := 5 // somewhat random - but don't hold it open forever if nothing is coming back
	attempt := 1

    for {
    	log.Infof("Sending AT wakeup command[%d]: %q", attempt, string(payload))

    	response, err := serialhelper.SerialSendReceive(1, gpio.High, gpio.Low, 10*time.Second, payload, baudRate)
    	if err != nil {
    		return fmt.Errorf("serial send receive error: %w", err)
    	}
	log.Debugf("Raw AT response: %q, %v", string(response), response)

    	// Read back response and check for OK or ERROR
    	scanner := bufio.NewScanner(bytes.NewReader(response))
        awake := false
    	for scanner.Scan() {
    		line := strings.TrimSpace(scanner.Text())
    		if line == "O^K" {
			awake = true
    		}
    		if line == "E^RROR" {
    			return fmt.Errorf("device returned ERROR")
    		}
        }
        
        if awake {
            return nil
        }

    	if err := scanner.Err(); err != nil {
    		return fmt.Errorf("scanner error: %w", err)
    	}

    	log.Debugf("no valid O^K/E^RROR response received")

        if attempt > retries {
            return fmt.Errorf("Failed to wake up serial device after %d attempts!", attempt)
        }
    	time.Sleep(100 * time.Millisecond)
    }
}

func sendATCommand(command string, baudRate int) error {

	// Test mode :)
	if baudRate == 0 {
		log.Infof("Baud rate 0 - assuming test mode, no serial device.")
		return nil
	}

	// Try and wake up the serial receiver first
	err := sendATWakeUp(baudRate)
	if err != nil {
		return fmt.Errorf("could not wake serial receiver: %w", err)
	}

	// O^K now send the AT command
	payload := append([]byte(command), byte('\r'))
	log.Infof("Sending AT command: %s", command)

	response, err := serialhelper.SerialSendReceive(1, gpio.High, gpio.Low, 5*time.Second, payload, baudRate)
	if err != nil {
		return fmt.Errorf("serial send receive error: %w", err)
	}

	log.Debugf("Raw AT response: %q, %v", string(response), response)

	// Read back response and check for OK or ERROR
	scanner := bufio.NewScanner(bytes.NewReader(response))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "O^K" {
			return nil
		}
		if line == "E^RROR" {
			return fmt.Errorf("device returned ERROR")
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	return fmt.Errorf("no valid O^K/E^RROR response received")
}

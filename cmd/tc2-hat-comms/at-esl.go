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
	baudRate int
}

func processATESL(config *CommsConfig, testClassification *TestClassification, eventChannel chan event) error {
	messenger := ATESLMessenger{
		baudRate: config.BaudRate,
	}

	if testClassification != nil {
		log.Println("Sending a test classification for AT ESL")
		testTrackingEvent := trackingEvent{
			Species: map[string]int32{
				testClassification.Animal: testClassification.Confidence,
			},
		}
		err := messenger.processTrackingEvent(testTrackingEvent)
		if err != nil {
			log.Error("Error processing test tracking event:", err)
		}
		return nil
	}

	for {
		log.Debug("Waiting")
		e := <-eventChannel
		log.Debugf("Found new event: %+v", e)

		// Process the event, depending on the type
		switch v := e.(type) {
		case trackingEvent:
			err := messenger.processTrackingEvent(v)
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
	log.Debugf("Processing battery event: %+v", b)
	// AT command, sending a battery reading as tenths of a volt
	atCmd := fmt.Sprintf("AT+CAMBAT=%d", int32(b.Voltage*10))

	err := sendATCommand(atCmd, a.baudRate)
	if err != nil {
		log.Error("Error sending battery reading:", err)
		return err
	}
	return nil
}

func (a ATESLMessenger) processTrackingEvent(t trackingEvent) error {
	log.Debugf("Processing tracking event: %+v", t)
	for animal, confidence := range t.Species {
		if confidence > 0 {
			// TODO: We will want to limit the number of classifications that we send as we can get up to one every frame (9 FPS)
			// Maybe something like only send a new classification if the confidence has increases.
			atCmd := fmt.Sprintf("AT+CAM=%s,%d", animal, confidence)

			err := sendATCommand(atCmd, a.baudRate)
			if err != nil {
				log.Error("Error sending classification:", err)
				return err
			}
		}
	}
	return nil
}

func sendATCommand(command string, baudRate int) error {
	log.Debugf("Sending command: %s", strings.TrimSpace(command))

	payload := append([]byte(command), byte('\r'), byte('\n'))

	response, err := serialhelper.SerialSendReceive(3, gpio.High, gpio.Low, 5*time.Second, payload, baudRate)
	if err != nil {
		return fmt.Errorf("serial send receive error: %w", err)
	}

	log.Debugf("Raw response: %s", strings.TrimSpace(string(response)))

	// Read back response and check for OK or ERROR
	scanner := bufio.NewScanner(bytes.NewReader(response))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "OK" {
			return nil
		}
		if line == "ERROR" {
			return fmt.Errorf("device returned ERROR")
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	return fmt.Errorf("no valid OK/ERROR response received")
}

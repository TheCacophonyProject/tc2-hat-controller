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

func processATESL(config *CommsConfig, testClassification *TestClassification, eventChannel chan event) error {
	messenger := ATESLMessenger{
		baudRate:    config.BaudRate,
		trapSpecies: config.TrapSpecies,
	}

	err := messenger.wakeUp()
	if err != nil {
		log.Info("Failed to wake up - try again:", err)
		return err
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

func (a ATESLMessenger) wakeUp() error {
	log.Debugf("Wake up serial device.")
	payload := []byte("\r\rAT\r")

	log.Debugf("Sending wakeup command: %q, baud rate: %d", string(payload), a.baudRate)

	err := serialhelper.SerialSend(1, gpio.High, gpio.Low, 5*time.Second, payload, a.baudRate)
	if err != nil {
		return fmt.Errorf("serial send error: %w", err)
	}
	time.Sleep(100 * time.Millisecond)
	return nil
}

func (a ATESLMessenger) processBatteryEvent(b batteryEvent) error {
	log.Info("Processing battery event")
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
	var (
		triggerAnimal     string = ""
		triggerConfidence int32  = 0
	)

	trigger := false
	for animal, conf := range t.Species {
		requiredConf, ok := a.trapSpecies[animal]
		if ok && conf >= requiredConf {
			trigger = true
			if conf > triggerConfidence {
				triggerAnimal = animal
				triggerConfidence = conf
			}
		}
	}

	// Only notify/trigger if we found a trap species with confidence in our track event
	if trigger {
		atCmd := fmt.Sprintf("AT+CAM=%s,%d", triggerAnimal, triggerConfidence)

		err := sendATCommand(atCmd, a.baudRate)
		if err != nil {
			log.Error("Error sending classification:", err)
			return err
		}
	}
	return nil
}

func sendATCommand(command string, baudRate int) error {

	// Wake-up first
	log.Debugf("Wake up serial device.")
	payload := []byte("\r\rAT\r")

	log.Debugf("Sending wakeup command: %q, baud rate: %d", string(payload), baudRate)

	err := serialhelper.SerialSend(1, gpio.High, gpio.Low, 5*time.Second, payload, baudRate)
	if err != nil {
		return fmt.Errorf("serial send error: %w", err)
	}
	time.Sleep(100 * time.Millisecond)

	// O^K now send the AT command
	payload = append([]byte(command), byte('\r'))
	log.Debugf("Sending command: %q", string(payload))

	response, err := serialhelper.SerialSendReceive(1, gpio.High, gpio.Low, 5*time.Second, payload, baudRate)
	if err != nil {
		return fmt.Errorf("serial send receive error: %w", err)
	}

	log.Debugf("Raw response: %q", string(response))

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

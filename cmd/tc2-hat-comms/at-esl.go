package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"time"
	"strconv"

	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"periph.io/x/conn/v3/gpio"
)

type ATESLMessenger struct {
	baudRate    int
	trapSpecies map[string]int32
}

type ATESLLastPrediction struct {
	What string
	When time.Time
	Lockout int64
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

	_, err := sendATCommand(atCmd, a.baudRate)
	if err != nil {
		log.Error("Error sending battery reading:", err)
		return err
	}
	return nil
}

func (a ATESLMessenger) processTrackingEvent(t trackingEvent, l *ATESLLastPrediction) error {

	log.Debugf("Received new tracking event What: %v, Confidence : %v, Region: %v, LastPredictionFrame: %v, Frame: %v",
                    t.What, t.Confidence, t.Region, t.LastPredictionFrame, t.Frame)

	if t.Frame != t.LastPredictionFrame {
		return nil
	}

	lastPrediction := time.Since(l.When).Seconds()

	// It's a prediction frame, but within the event lockout - skip notifying
	if lastPrediction < float64(l.Lockout) {
		log.Infof("Skipping prediction of %v - within event lockout %vs (%d)", l.What, lastPrediction, l.Lockout)
		return nil
	}

	log.Infof("Processing tracking prediction (frame) event What: %v, Confidence : %v, Region: %v, Frame: %v",
                    t.What, t.Confidence, t.Region, t.Frame)

	var targetConfidence int32 = 0
	target := false
	// We've found an object - is it a target (trapable) species?
	if _, found := a.trapSpecies["any"]; found {

		// We can do without false-positives, not quite any :)
		if t.What == "false-positive" {
			return nil
		}

		target = true
		targetConfidence = a.trapSpecies["any"]

	} else if _, found := a.trapSpecies[t.What]; found {
		target = true
		targetConfidence = a.trapSpecies[t.What]
	}

	if target && t.Confidence >= targetConfidence {
		log.Infof("Track prediction of a target species with confidence: %s,%d", t.What, t.Confidence)

		atCmd := fmt.Sprintf("AT+CAM=%s,%d", t.What, t.Confidence)
		l.What = t.What   	// Remember the object
		l.When = time.Now()	// Remember when we detected it

		_, err := sendATCommand(atCmd, a.baudRate)
		if err != nil {
			log.Error("Error sending classification:", err)
			return err
		}

		// Now let's check the event lockout
		l.Lockout = getEventLockout(a.baudRate)
	}

	return nil
}

func sendATWakeUp(baudRate int) error {

	log.Debugf("Wake up serial device.")
	payload := []byte("\r\rAT\r")

	retries := 0 // Don't retry (for now)
	attempt := 1

	for {
		log.Infof("Sending AT wakeup command[%d]: %q", attempt, string(payload))

		err := serialhelper.SerialSend(1, gpio.High, gpio.Low, 10*time.Second, payload, baudRate)
	        attempt = attempt + 1

		// response, err := serialhelper.SerialSendReceive(1, gpio.High, gpio.Low, 10*time.Second, payload, baudRate)
		if err != nil {
			return fmt.Errorf("serial send error: %w", err)
		}
		if attempt > retries {
			return nil
			// Don't error - just carry on
			// return fmt.Errorf("Failed to wake up serial device after %d attempts!", attempt)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func sendATCommand(command string, baudRate int) ([]byte, error) {

	response := []byte("")

	// Test mode :)
	if baudRate == 0 {
		log.Infof("Baud rate 0 - assuming test mode, no serial device.")
		return response, nil
	}

	// Try and wake up the serial receiver first
	err := sendATWakeUp(baudRate)
	if err != nil {
		return response, fmt.Errorf("could not wake serial receiver: %w", err)
	}

	// O^K now send the AT command
	payload := append([]byte(command), byte('\r'))
	log.Infof("Sending AT command: %s", command)

	response, err = serialhelper.SerialSendReceive(1, gpio.High, gpio.Low, 5*time.Second, payload, baudRate)
	if err != nil {
		return response, fmt.Errorf("serial send receive error: %w", err)
	}

	log.Debugf("Raw AT response: %q, %v", string(response), response)

	// Read back response and check for OK or ERROR
	scanner := bufio.NewScanner(bytes.NewReader(response))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "E^RROR" {
			return response, fmt.Errorf("device returned ERROR")
		}
	}

	if err := scanner.Err(); err != nil {
		return response, fmt.Errorf("scanner error: %w", err)
	}

	return response, nil
}

/*
 
    EVENT_LOCKOUT_MINS
    Time in minutes to have an event lockout; default 30mins. Activated on an event.

    2min = 'w0502’
    10min = 'w050a’
    30min = 'w051e’

 */

func getEventLockout(baudRate int) int64 {

	var lockout_minutes_default int64 = 30 // default 30mins.

	cmd := append([]byte("AT+XCMD=m00"), calcCRC16([]byte("m00"))...)
	log.Infof("get event lockout via command %v", cmd)

	response, _ := sendATCommand(string(cmd), baudRate)

	// w05..
	// response.. \xa5\xfc\r\nm00\r\n\r\n00: ff ff ff ff ff 02
	seq := [3]byte{'0', '0', ':'} // First row of node register block - 00:
	pos := 0

	for i := 0; i <= len(response)-3; i++ {
		if response[i] == seq[0] && response[i+1] == seq[1] && response[i+2] == seq[2] {
			log.Debugf("Found %v - position: %d", seq, i)
			pos = i + 3 + 5 * 3 + 1 // aka it's the 6th element + drop the leading space
			break
		}
	}

	hexstr := string(response[pos:pos+2])
	lockout_minutes, err := strconv.ParseInt(hexstr, 16, 64)
	log.Debugf("Converted %v to lockout minutes: %d", hexstr, lockout_minutes)

	if err != nil {
		log.Errorf("parseInt error: %w", err)
		lockout_minutes = 0
	}
	if lockout_minutes == 0 {
		lockout_minutes = lockout_minutes_default
		log.Infof("Lockout time not set - using default (%d)", lockout_minutes_default)
	}

	lockout_seconds := lockout_minutes * 60
	log.Infof("Lockout time = %d (s)", lockout_seconds)

	return lockout_seconds
}

func feedCRC16(crc uint16, dat byte) uint16 {
	for i := 0; i < 8; i++ {
		bit0 := (crc ^ uint16(dat)) & 1
		crc >>= 1
		if bit0 == 1 {
			crc ^= 0x8408
		}
		dat >>= 1
	}
	return crc
}

func calcCRC16(msg []byte) []byte {
	crc := uint16(0xFFFF)
	for _, b := range msg {
		crc = feedCRC16(crc, b)
	}
	return []byte{byte(crc & 0xFF), byte(crc >> 8)}
}

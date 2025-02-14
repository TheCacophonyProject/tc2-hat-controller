package main

import (
	"fmt"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

// processSimpleOutput will just output HIGH or LOW to the UART TX pin for showing if the
// trap should be active or not.
func processSimpleOutput(config *CommsConfig, trackingSignals chan trackingEvent) error {
	// Initialize the periph host drivers
	if _, err := host.Init(); err != nil {
		return fmt.Errorf("failed to initialize periph: %v", err)
	}

	log.Info("Get lock on serial port")
	if config.CommsOut == "uart" || config.CommsOut == "simple" {
		serialFile, err := serialhelper.GetSerial(3, gpio.High, gpio.Low, time.Second)
		if err != nil {
			return err
		}
		defer serialhelper.ReleaseSerial(serialFile)
	}
	log.Info("Done")

	// Set up the GPIO pins
	outPin := gpioreg.ByName(config.UartTxPin)
	log.Debugf("Setting output pin '%s'", config.UartTxPin)
	if outPin == nil {
		return fmt.Errorf("failed to find out pin '%s'", config.UartTxPin)
	}
	if err := outPin.Out(gpio.Low); err != nil {
		return fmt.Errorf("failed to set out pin low: %v", err)
	}

	trapEnabled := false
	previousTrapEnabled := false
	lastProtectSpeciesSighting := time.Time{}
	lastTrapSpeciesSighting := time.Time{}

	for {
		now := time.Now()
		trapEnabled = config.TrapEnabledByDefault

		// Check if species sighting influences trap state
		if lastProtectSpeciesSighting.Add(config.ProtectDuration).After(now) {
			trapEnabled = false // Disable trap if protective species has been sighted recently
		} else if lastTrapSpeciesSighting.Add(config.TrapDuration).After(now) {
			trapEnabled = true // Enable trap if trap species has been sighted recently
		}

		// Check if the state has changed and if so, enable or disable the trap
		if trapEnabled != previousTrapEnabled {
			if trapEnabled {
				log.Info("Enabling trap")
				if err := outPin.Out(gpio.High); err != nil {
					return fmt.Errorf("failed to set out pin high: %v", err)
				}
				eventclient.AddEvent(eventclient.Event{
					Timestamp: time.Now(),
					Type:      "enablingTrap",
				})
			} else {
				log.Info("Disabling trap")
				if err := outPin.Out(gpio.Low); err != nil {
					return fmt.Errorf("failed to set out pin low: %v", err)
				}
				eventclient.AddEvent(eventclient.Event{
					Timestamp: time.Now(),
					Type:      "disablingTrap",
				})
			}
		}

		previousTrapEnabled = trapEnabled

		// Delay 10 seconds or until the trap should be disabled
		var delay = 10 * time.Second
		trapDisableTime := lastTrapSpeciesSighting.Add(config.TrapDuration)
		if trapEnabled && time.Until(trapDisableTime) < delay {
			delay = time.Until(trapDisableTime)
		}

		log.Debug("Waiting")
		select {
		case t := <-trackingSignals:
			log.Debugf("Found new track: %+v", t)
			if t.Species.MatchSpeciesWithConfidence(config.ProtectSpecies) {
				log.Debug("Found an animal that needs to be protected")
				lastProtectSpeciesSighting = time.Now()
			} else if t.Species.MatchSpeciesWithConfidence(config.TrapSpecies) {
				log.Debug("Found an animal that needs to be trapped")
				lastTrapSpeciesSighting = time.Now()
			} else {
				log.Debug("No animals need to be protected or trapped, not changing trap state.")
			}

		case <-time.After(delay):
			log.Debug("Scheduled check")
		}
	}
}

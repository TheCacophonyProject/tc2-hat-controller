// This section deals with communication with peripherals with simple digital signals.

package main

import (
	"fmt"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

func processDigital(config *CommsConfig, trackingSignals chan trackingEvent) error {
	// Initialize the periph host drivers
	if _, err := host.Init(); err != nil {
		return fmt.Errorf("failed to initialize periph: %v", err)
	}

	// Set up the GPIO pins
	outPin := gpioreg.ByName(config.UartTxPin)
	if outPin == nil {
		return fmt.Errorf("failed to find out pin '%s'", config.UartTxPin)
	}
	if err := outPin.Out(gpio.Low); err != nil {
		return fmt.Errorf("failed to set out pin low: %v", err)
	}

	trapActive := false
	previousTrapActive := false
	lastProtectSpeciesSighting := time.Time{}
	lastTrapSpeciesSighting := time.Time{}

	for {
		now := time.Now()
		trapActive = config.TrapEnabledByDefault

		// Check if species sighting influences trap state
		if lastProtectSpeciesSighting.Add(config.ProtectDuration).After(now) {
			trapActive = false // Disable trap if protective species has been sighted recently
		} else if lastTrapSpeciesSighting.Add(config.TrapDuration).After(now) {
			trapActive = true // Enable trap if trap species has been sighted recently
		}

		// Check if the state has changed and if so, activate or deactivate the trap
		if trapActive != previousTrapActive {
			if trapActive {
				log.Info("Activating trap")
				if err := outPin.Out(gpio.High); err != nil {
					return fmt.Errorf("failed to set out pin high: %v", err)
				}
			} else {
				log.Info("Deactivating trap")
				if err := outPin.Out(gpio.Low); err != nil {
					return fmt.Errorf("failed to set out pin low: %v", err)
				}
			}
		}

		previousTrapActive = trapActive

		// Delay 10 seconds or until the trap should be deactivated
		var delay = 10 * time.Second
		trapDeactivateTime := lastTrapSpeciesSighting.Add(config.TrapDuration)
		if trapActive && time.Until(trapDeactivateTime) < delay {
			delay = time.Until(trapDeactivateTime)
		}

		log.Debug("Waiting")
		select {
		case t := <-trackingSignals:
			log.Debugf("Found new track: %+v", t)
			if t.species.MatchSpeciesWithConfidence(config.ProtectSpecies) {
				log.Debug("Found an animal that needs to be protected")
				lastProtectSpeciesSighting = time.Now()
			} else if t.species.MatchSpeciesWithConfidence(config.TrapSpecies) {
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

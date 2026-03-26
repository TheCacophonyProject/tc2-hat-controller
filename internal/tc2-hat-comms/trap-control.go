package comms

import (
	"fmt"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
)

// processTrapControl communicates the trap enabled/disabled state by writing
// the "enable" variable over UART instead of setting a digital pin.
func processTrapControl(config *CommsConfig, eventSignals chan event) error {
	messenger := UartMessenger{
		baudRate: config.BaudRate,
	}

	trapEnabled := false
	previousTrapEnabled := false
	lastProtectSpeciesSighting := time.Time{}
	lastTrapSpeciesSighting := time.Time{}
	enablingReason := ""
	disablingReason := ""

	recordingStartTime := time.Time{}
	trackStartTime := time.Time{}
	triggerAnimal := ""
	var confidence int32

	for {
		now := time.Now()
		trapEnabled = config.TrapEnabledByDefault

		if lastProtectSpeciesSighting.Add(config.ProtectDuration).After(now) {
			trapEnabled = false
		} else if lastTrapSpeciesSighting.Add(config.TrapDuration).After(now) {
			trapEnabled = true
		}

		if trapEnabled != previousTrapEnabled {
			if trapEnabled {
				log.Infof("Enabling trap: %s", enablingReason)
				if err := messenger.sendWriteMessage("enable", true); err != nil {
					return fmt.Errorf("failed to write enable=true: %v", err)
				}
				trapEnableTime := time.Now()
				log.Infof("Recording start time: %s", recordingStartTime.Format("15:04:05.999"))
				log.Infof("Track start time: %s", trackStartTime.Format("15:04:05.999"))
				log.Infof("TrapEnableTime: %s", trapEnableTime.Format("15:04:05.999")) // TODO, we can get better accuracy on when this actually
				timeToEnableTrap := trapEnableTime.Sub(recordingStartTime).String()
				log.Infof("Time to enable trap: %s", timeToEnableTrap)
				eventclient.AddEvent(eventclient.Event{
					Timestamp: time.Now(),
					Type:      "enablingTrap",
					Details: map[string]any{
						"reason":             enablingReason,
						"recordingStartTime": recordingStartTime,
						"trackStartTime":     trackStartTime,
						"trapEnableTime":     trapEnableTime,
						"timeToEnableTrap":   timeToEnableTrap,
						"animal":             triggerAnimal,
						"confidence":         confidence,
					},
				})
			} else {
				log.Info("Disabling trap: ", disablingReason)
				if err := messenger.sendWriteMessage("active", false); err != nil {
					return fmt.Errorf("failed to write enable=false: %v", err)
				}
				eventclient.AddEvent(eventclient.Event{
					Timestamp: time.Now(),
					Type:      "disablingTrap",
					Details: map[string]any{
						"reason": disablingReason,
					},
				})
			}
		}

		previousTrapEnabled = trapEnabled

		var delay = 10 * time.Second
		trapDisableTime := lastTrapSpeciesSighting.Add(config.TrapDuration)
		if trapEnabled && time.Until(trapDisableTime) < delay {
			delay = time.Until(trapDisableTime)
		}

		disablingReason = "timeout"
		enablingReason = "timeout"
		log.Debug("Waiting")
		select {
		case t := <-eventSignals:
			switch v := t.(type) {
			case trackingEvent:
				log.Debugf("Received tracking event: %+v", v)
				trackStartTime = v.TrackStartTime

				protect, animal, conf := v.Species.MatchSpeciesWithConfidence(config.ProtectSpecies)
				if protect {
					disablingReason = fmt.Sprintf("Found an %s of confidence %d that needs to be protected", animal, conf)
					log.Debug(disablingReason)
					lastProtectSpeciesSighting = time.Now()
					break
				}

				trap, animal, conf := v.Species.MatchSpeciesWithConfidence(config.TrapSpecies)
				if trap {
					enablingReason = fmt.Sprintf("Found an %s of confidence %d that needs to be trapped", animal, conf)
					triggerAnimal = animal
					confidence = conf
					log.Debug(enablingReason)
					lastTrapSpeciesSighting = time.Now()
					break
				}

				log.Debug("No animals need to be protected or trapped, not changing trap state.")

			case recordingEvent:
				log.Debugf("Received recording event: %+v", v)
				if v.Recording {
					recordingStartTime = v.Timestamp
				} else {
					recordingStartTime = time.Time{}
				}

			default:
				log.Debugf("Ignoring non tracking event: %+v", t)
				continue
			}

		case <-time.After(delay):
			log.Debug("Scheduled check")
		}
	}
}

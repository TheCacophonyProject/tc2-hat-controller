package main

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/godbus/dbus"
)

func getBatteryType(batteryConfig *goconfig.Battery, voltage float32) (*goconfig.BatteryType, error) {
	// Get battery if custom battery chemistry is used
	if batteryConfig.CustomBatteryType != nil {
		return batteryConfig.CustomBatteryType, nil
	}

	// Get battery type if preset battery type is set
	if batteryConfig.PresetBatteryType != "" {
		for _, battery := range goconfig.PresetBatteryTypes {
			if strings.EqualFold(battery.Name, batteryConfig.PresetBatteryType) {
				return &battery, nil
			}
		}
		return nil, fmt.Errorf("unknown preset battery type %s", batteryConfig.PresetBatteryType)
	}

	// Guess battery type from voltage
	for _, battery := range goconfig.PresetBatteryTypes {
		if voltage >= battery.MinVoltage && voltage <= battery.MaxVoltage {
			return &battery, nil
		}
	}

	// No battery found to match.
	return nil, nil
}

func getBatteryPercent(batteryConfig *goconfig.Battery, hvBat float32, lvBat float32) (float32, string, float32) {
	var batVolt float32
	if hvBat <= lvBatThresh {
		batVolt = lvBat
	} else {
		batVolt = hvBat
	}
	if batVolt < batteryConfig.MinimumVoltageDetection {
		return 100, "no_battery_voltage", 0 // No battery voltage supplied
	}

	batteryType, err := getBatteryType(batteryConfig, batVolt)
	if err != nil {
		log.Error(err)
		return 100, "unknown_battery_type", batVolt
	}
	if batteryType == nil {
		return 100, "unknown_battery_type", batVolt
	}

	var upper float32 = 0
	var lower float32 = 0
	var i = 0
	voltages := batteryType.Voltages
	percents := batteryType.Percent
	for i = 0; i < len(voltages); i++ {
		voltage := voltages[i]
		lower = upper
		upper = voltage
		if batVolt >= lower && batVolt < upper {
			break
		}
		if batVolt <= lower && batVolt <= upper {
			// probably have wrong battery config
			log.Printf("Could not find a matching voltage range in config for %vV", batVolt)
			return percents[i], batteryType.Name, batVolt
		}
	}
	if i == 0 {
		return 0, batteryType.Name, batVolt
	} else if batVolt > upper {
		//voltage is higher than config
		return 100, batteryType.Name, batVolt
	}
	gradient := (percents[i] - percents[i-1]) / (upper - lower)
	batteryPercent := gradient*batVolt + percents[i-1] - gradient*lower
	return batteryPercent, batteryType.Name, batVolt
}

func monitorVoltageLoop(a *attiny, config *goconfig.Config) {
	batteryConfig := goconfig.DefaultBattery()
	if err := config.Unmarshal(goconfig.BatteryKey, &batteryConfig); err != nil {
		return
	}
	err := keepLastLines("/var/log/battery-readings.csv", batteryMaxLines)
	if err != nil {
		log.Printf("Could not truncate /var/log/battery-readings.csv %v", err)
	}
	var batteryPercent float32 = -1.0
	startTime := time.Now()
	i := 5
	for {
		hvBat, err := a.readHVBattery()
		if err != nil {
			log.Error(err)
			continue
		}
		lvBat, err := a.readLVBattery()
		if err != nil {
			log.Error(err)
			continue
		}
		rtcBat, err := a.readRTCBattery()
		if err != nil {
			log.Error(err)
			continue
		}
		if time.Since(startTime) > time.Duration(24*time.Hour) {
			err := keepLastLines(batteryReadingsFile, batteryMaxLines)
			if err != nil {
				//not sure why it would error but should we keep trying...
				log.Printf("Could not truncate /var/log/battery-readings.csv %v", err)
			} else {
				startTime = time.Now()
			}
		}
		file, err := os.OpenFile(batteryReadingsFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
		if err != nil {
			log.Fatal(err)
		}
		line := fmt.Sprintf("%s, %.2f, %.2f, %.2f", time.Now().Format("2006-01-02 15:04:05"), hvBat, lvBat, rtcBat)
		if i >= 5 {
			log.Println("Battery reading:", line)
			i = 0
		}
		i++
		_, err = file.WriteString(line + "\n")
		file.Close()
		if err != nil {
			log.Fatal(err)
		}
		newPercent, batteryType, voltage := getBatteryPercent(&batteryConfig, hvBat, lvBat)
		if batteryPercent == -1 || math.Abs(float64(batteryPercent-newPercent)) >= 10 {
			//log battery percent
			batteryPercent = newPercent
			err := eventclient.AddEvent(eventclient.Event{
				Timestamp: time.Now(),
				Type:      "rpiBattery",
				Details: map[string]interface{}{
					"battery":     math.Round((float64(batteryPercent))),
					"batteryType": batteryType,
					"voltage":     voltage,
				},
			})
			if err != nil {
				log.Error("Error sending battery event:", err)
			}

			err = sendBatterySignal(float64(voltage), float64(batteryPercent))
			if err != nil {
				log.Error("Error sending battery signal:", err)
			}
			log.Infof("New battery event: type=%s, voltage=%v, percent=%v", batteryType, voltage, batteryPercent)
		}
		time.Sleep(2 * time.Minute)
	}
}

// makeBatteryReadings is a debugging tool for reading battery voltages
func makeBatteryReadings(attiny *attiny) error {
	log.Info("Starting battery reading loop.")
	readings := 60
	rawValues := make([]uint16, readings)
	rawDiffs := make([]uint16, readings)
	var err error
	for i := 0; i < readings; i++ {

		rawValues[i], rawDiffs[i], err = attiny.readBattery(batteryHVDivVal1Reg, batteryHVDivVal2Reg)
		if err != nil {
			log.Error(err)
			continue
		}
		log.Infof("Making reading %d out of %d", i+1, readings)
		time.Sleep(1 * time.Second)
		continue
		/*
			hvBat, err := attiny.readMainBattery()
			if err != nil {
				log.Error(err)
				continue
			}
			log.Infof("Main battery voltage: %v", hvBat)
			lvBat, err := attiny.readLVBattery()
			if err != nil {
				log.Error(err)
				continue
			}
			log.Info("Low voltage battery voltage: ", lvBat)
			rtcBat, err := attiny.readRTCBattery()
			if err != nil {
				log.Error(err)
				continue
			}
			log.Info("RTC battery voltage: ", rtcBat)

			time.Sleep(5 * time.Second)
		*/
	}

	rawSD := calculateStandardDeviation(rawValues)
	rawMean := calculateMean(rawValues)
	diffSD := calculateStandardDeviation(rawDiffs)
	diffMean := calculateMean(rawDiffs)

	log.Infof("Raw SD: %.2f, Raw Mean: %.2f, Diff SD: %.2f, Diff Mean: %.2f", rawSD, rawMean, diffSD, diffMean)
	return nil
}

func sendBatterySignal(voltage, percent float64) error {
	// Connect to the system bus
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}

	// Request a name on the bus (required for sending signals)
	const busName = "org.cacophony.attiny.Sender"
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return err
	}

	// Define the signal
	sig := &dbus.Signal{
		Path: dbus.ObjectPath("/org/cacophony/attiny"),
		Name: "org.cacophony.attiny.Battery",
		Body: []interface{}{voltage, percent},
	}

	// Emit the signal
	conn.Emit(sig.Path, sig.Name, sig.Body...)
	log.Printf("Emitted battery signal: voltage=%.2f, percent=%.2f", float64(voltage), float64(percent))

	return nil
}

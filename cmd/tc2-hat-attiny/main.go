/*
tc2-hat-controller - Communicates with TC2 hat
Copyright (C) 2023, The Cacophony Project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.
*/

package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/rpi-net-manager/netmanagerclient"
	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"github.com/alexflint/go-arg"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

const (
	initialGracePeriod         = 5 * time.Minute
	saltCommandMaxWaitDuration = 30 * time.Minute
	saltCommandWaitDuration    = time.Minute
	batteryMaxLines            = 20000
	lvBatThresh                = 15
	batteryReadingsFile        = "/var/log/battery-readings.csv"
)

var (
	version = "<not set>"

	maxTxAttempts      = 5
	txRetryInterval    = time.Second
	mu                 sync.Mutex
	stayOnUntil        = time.Now()
	stayOnLock         sync.Mutex
	stayOnForProcess   = map[string]time.Time{}
	saltCommandWaitEnd = time.Time{}
	log                = logging.NewLogger("info")
)

type Args struct {
	ConfigDir          string `arg:"-c,--config" help:"configuration folder"`
	SkipWait           bool   `arg:"-s,--skip-wait" help:"will not wait for the date to update"`
	Timestamps         bool   `arg:"-t,--timestamps" help:"include timestamps in log output"`
	SkipSystemShutdown bool   `arg:"--skip-system-shutdown" help:"don't shut down operating system when powering down"`
	BatteryReading     bool   `arg:"--battery-reading" help:"Run helper code to read battery voltage."`

	logging.LogArgs
}

func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{
		ConfigDir: goconfig.DefaultConfigDir,
	}
	arg.MustParse(&args)
	return args
}

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err)
	}
}

func runMain() error {
	args := procArgs()

	log = logging.NewLogger(args.LogLevel)

	config, err := goconfig.New(args.ConfigDir)
	if err != nil {
		return err
	}

	log.Printf("Running version: %s", version)
	log.Printf("Expecting ATtiny version v%s.%s.%s", attinyMajorStr, attinyMinorStr, attinyPatchStr)

	_, err = host.Init()
	if err != nil {
		return err
	}

	log.Println("Connecting to ATtiny.")
	attiny, err := connectToATtinyWithRetries(10)
	if err != nil {
		return err
	}

	if args.BatteryReading {
		err := makeBatteryReadings(attiny)
		if err != nil {
			log.Error(err)
		}
		return err
	}

	log.Info("Starting DBus service.")
	if err := startService(attiny); err != nil {
		return err
	}

	go func() {
		for {
			if err := attiny.checkForConnectionStateUpdates(); err != nil {
				log.Printf("Error checking for connection state updates: %s", err)
				time.Sleep(time.Second)
			}
		}
	}()

	go monitorVoltageLoop(attiny, config)
	go checkATtinySignalLoop(attiny)

	attiny.readCameraState()
	log.Println(attiny.CameraState)

	waitDuration := time.Duration(0)
	previousOnReason := ""
	onReason := ""
	if args.SkipWait {
		log.Println("Not waiting initial grace period.")
	} else {
		waitDuration = initialGracePeriod
		onReason = fmt.Sprintf("Waiting initial grace period of %s", durToStr(waitDuration))
	}

	for {
		stayOnUntilDuration := time.Until(stayOnUntil)
		if stayOnUntilDuration > waitDuration {
			waitDuration = stayOnUntilDuration
			onReason = fmt.Sprintf("Staying on because camera has been requested to stay on for %s", durToStr(waitDuration))
		}

		// Check if the RP2040 wants the RPi to stay on
		if waitDuration <= time.Duration(0) {
			val, err := attiny.readRegister(rp2040PiPowerCtrlReg)
			if err != nil {
				return err
			}
			if (val & 0x01) == 0x01 {
				onReason = "Staying on because RP2040 wants me to stay on"
				waitDuration = 10 * time.Second
			}
		}

		// Checking if a salt command is running should only be done if needed
		if waitDuration < time.Duration(0) && shouldStayOnForSalt() {
			waitDuration = saltCommandWaitDuration
			onReason = "Staying on because salt command is running"
		}

		if waitDuration <= time.Duration(0) {
			stayOnLock.Lock()
			for process, maxTime := range stayOnForProcess {
				if time.Now().After(maxTime) {
					log.Printf("Max stay on time reached for %v", process)
					delete(stayOnForProcess, process)
				} else {
					onReason = fmt.Sprintf("Staying on for %v", process)
					waitDuration = 10 * time.Second
					break
				}
			}
			stayOnLock.Unlock()
		}

		if waitDuration <= time.Duration(0) {
			log.Println("No longer needed to be powered on, powering off")
			time.Sleep(1 * time.Second)
			if err := shutdown(attiny); err != nil {
				return err
			}
			time.Sleep(time.Second * 3)
			return nil
		}

		// TODO Make this a timeout switch with a channel trigger also so the
		if previousOnReason != onReason {
			log.Println(onReason)
			previousOnReason = onReason
		}
		time.Sleep(waitDuration)
		waitDuration = time.Duration(0)
	}
}

// keepLastLines keeps the last `maxLines` lines of the specified file.
func keepLastLines(filePath string, maxLines int) error {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil
	}
	tmpFile := filepath.Join(os.TempDir(), filepath.Base(filePath)+".tmp")
	cmd := exec.Command("sh", "-c", fmt.Sprintf("tail -n %d %s > %s", maxLines, filePath, tmpFile))
	if err := cmd.Run(); err != nil {
		return err
	}
	return os.Rename(tmpFile, filePath)
}

func getBatteryPercent(batteryConfig *goconfig.Battery, hvBat float32, lvBat float32) (float32, string, float32) {
	var batVolt float32
	if hvBat <= lvBatThresh {
		batVolt = lvBat
	} else {
		batVolt = hvBat
	}

	if batVolt < 1 {
		batVolt = 0
	}

	batType, voltages, percents := batteryConfig.GetBatteryVoltageThresholds(batVolt)

	if batVolt == 0 {
		return 100, batType, 0
	}

	var upper float32 = 0
	var lower float32 = 0
	var i = 0
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
			return percents[i], batType, batVolt
		}
	}
	if i == 0 {
		return 0, batType, batVolt
	} else if batVolt > upper {
		//voltage is higher than config
		return 100, batType, batVolt
	}
	gradient := (percents[i] - percents[i-1]) / (upper - lower)
	batteryPercent := gradient*batVolt + percents[i-1] - gradient*lower
	return batteryPercent, batType, batVolt
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
		hvBat, err := a.readMainBattery()
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
			eventclient.AddEvent(eventclient.Event{
				Timestamp: time.Now(),
				Type:      "rpiBattery",
				Details: map[string]interface{}{
					"battery":     math.Round((float64(batteryPercent))),
					"batteryType": batteryType,
					"voltage":     voltage,
				},
			})
		}
		time.Sleep(2 * time.Minute)
	}
}

func checkATtinySignalLoop(a *attiny) {
	pinName := "GPIO16" //TODO add pin to config
	pin := gpioreg.ByName(pinName)
	if pin == nil {
		log.Printf("Failed to find {%s}", pinName)
	}
	pin.In(gpio.PullUp, gpio.FallingEdge)
	log.Println("Starting check ATtiny signal loop")
	for {
		pin.Read()
		if pin.Read() == gpio.High {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		log.Println("Signal from ATtiny")
		for {
			if a.CameraState != statePoweringOff {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		piCommands, err := a.readPiCommands(true)
		if err != nil {
			log.Println("Error reading pi commands:", err)
			continue
		}

		//TODO Fix bug causing this instead to be triggered twice, error is probably in ATtiny code
		log.Printf("Commands register: %x\n", piCommands)
		if piCommands == 0 {
			log.Println("No command flags set, writing camera state and connection state.")
			if err := a.writeCameraState(a.CameraState); err != nil {
				log.Printf("Error writing camera state: %s", err)
			}
			if err := a.writeConnectionState(a.ConnectionState); err != nil {
				log.Printf("Error writing connection state: %s", err)
			}
		}
		if isFlagSet(piCommands, WriteCameraStateFlag) {
			log.Println("write camera state flag")
			if err := a.writeCameraState(a.CameraState); err != nil {
				log.Printf("Error writing camera state: %s", err)
			}
		}

		if isFlagSet(piCommands, ReadErrorsFlag) {
			log.Println("Read attiny errors flag set")
			readAttinyErrors(a)
		}

		if isFlagSet(piCommands, EnableWifiFlag) {
			log.Println("Enable wifi flag set.")
			enableWifi()
		}

		if isFlagSet(piCommands, PowerDownFlag) {
			log.Println("Power down flag set.")
			log.Println("TODO, make sure device has finished its business before powering down.")
			log.Println("Shutting down.")
			shutdown(a)
			time.Sleep(time.Second * 3)
		}

		if isFlagSet(piCommands, ToggleAuxTerminalFlag) {
			log.Println("Toggle aux terminal flag set.")
			if serialhelper.SerialInUseFromTerminal() {
				_, err := exec.Command("disable-aux-uart").CombinedOutput()
				if err != nil {
					log.Println("Error disabling aux uart:", err)
				}
			} else {
				_, err := exec.Command("enable-aux-uart").CombinedOutput()
				if err != nil {
					log.Println("Error enabling aux uart:", err)
				}
			}
			a.writeAuxState()
		}

		time.Sleep(time.Second)
	}
}

func isFlagSet(command, flag uint8) bool {
	return (command & flag) != 0
}

func enableWifi() {
	err := netmanagerclient.EnableWifi(true)
	if err != nil {
		log.Println("Error enabling wifi:", err)
	}
}

func readAttinyErrors(a *attiny) {
	log.Println("Reading Attiny errors.")
	errorCodes, err := a.checkForErrorCodes(true)
	if err != nil {
		log.Println("Error checking for errors on ATtiny:", err)
	}

	errorStrs := []string{}
	for _, err := range errorCodes {
		errorStrs = append(errorStrs, err.String())
	}

	if len(errorStrs) > 0 {
		event := eventclient.Event{
			Timestamp: time.Now(),
			Type:      "ATtinyError",
			Details: map[string]interface{}{
				"error": errorStrs,
			},
		}
		log.Println("ATtiny Errors:", errorStrs)
		err := eventclient.AddEvent(event)
		if err != nil {
			log.Println("Error adding event:", err)
		}
	}

	// Run specific checks for some errors
	for _, err := range errorCodes {
		switch err {
		case INVALID_CAMERA_STATE:
			if err := a.readCameraState(); err != nil {
				log.Println("Error reading camera state:", err)
			}
			if err := a.writeCameraState(statePoweredOn); err != nil {
				log.Println("Error writing camera state:", err)
			}
		}
	}
}

func setStayOnUntil(newTime time.Time) error {
	if time.Until(newTime) > 12*time.Hour {
		return errors.New("can not delay over 12 hours")
	}
	mu.Lock()
	defer mu.Unlock()

	if stayOnUntil.Before(newTime) {
		stayOnUntil = newTime
		//log.Println("Staying on until", stayOnUntil.Format(time.DateTime))
	}
	return nil
}

func stayOnFinished(processName string) {
	stayOnLock.Lock()
	defer stayOnLock.Unlock()
	delete(stayOnForProcess, processName)
}

func setStayOnForProcess(processName string, maxTime time.Time) error {
	if time.Until(maxTime) > 12*time.Hour {
		return errors.New("can not delay over 12 hours")
	}
	stayOnLock.Lock()
	defer stayOnLock.Unlock()
	if stayOnUntil.Before(maxTime) {
		stayOnForProcess[processName] = maxTime
	} else {
		delete(stayOnForProcess, processName)
	}
	return nil
}

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

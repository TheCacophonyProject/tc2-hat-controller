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
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	"github.com/TheCacophonyProject/go-config"
	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/rpi-net-manager/netmanagerclient"
	serialhelper "github.com/TheCacophonyProject/tc2-hat-controller"
	arg "github.com/alexflint/go-arg"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

const (
	initialGracePeriod         = 5 * time.Minute
	saltCommandMaxWaitDuration = 30 * time.Minute
	saltCommandWaitDuration    = time.Minute
)

var (
	version = "<not set>"

	maxTxAttempts   = 5
	txRetryInterval = time.Second

	mu                 sync.Mutex
	stayOnUntil        = time.Now()
	saltCommandWaitEnd = time.Time{}
)

type Args struct {
	ConfigDir          string `arg:"-c,--config" help:"configuration folder"`
	SkipWait           bool   `arg:"-s,--skip-wait" help:"will not wait for the date to update"`
	Timestamps         bool   `arg:"-t,--timestamps" help:"include timestamps in log output"`
	SkipSystemShutdown bool   `arg:"--skip-system-shutdown" help:"don't shut down operating system when powering down"`
	Write              *Write `arg:"subcommand:write"`
	Read               *Read  `arg:"subcommand:read"`
}

type Write struct {
	Reg string `arg:"required" help:"The Register you want to write to, in hex (0xnn)"`
	Val string `arg:"required" help:"The value you want to write, in hex (0xnn)"`
}

type Read struct {
	Reg string `arg:"required" help:"The Register you want to read from, in hex (0xnn)"`
}

func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{
		ConfigDir: config.DefaultConfigDir,
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
	config, err := goconfig.New(args.ConfigDir)
	if err != nil {
		return err
	}

	if !args.Timestamps {
		log.SetFlags(0)
	}

	log.Printf("Running version: %s", version)

	_, err = host.Init()
	if err != nil {
		return err
	}

	log.Println("Connecting to ATtiny.")
	attiny, err := connectToATtinyWithRetries(10)
	if err != nil {
		return err
	}
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

	/*
		go func() {
			for {
				alarmTime, err := rtc.ReadAlarmTime()
				if err != nil {
					log.Printf("Failed to read alarm time: %s", err)
				} else {
					log.Println("Alarm time:", alarmTime)
				}
				alarmEnabled, err := rtc.ReadAlarmEnabled()
				if err != nil {
					log.Printf("Failed to read alarm enabled: %s", err)
				} else {
					log.Println("Alarm enabled:", alarmEnabled)
				}
				time.Sleep(time.Minute * 5)
			}
		}()
	*/

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

		if waitDuration < saltCommandWaitDuration && shouldStayOnForSalt() {
			waitDuration = saltCommandWaitDuration
			onReason = "Staying on because salt command is running"
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

const limeBatteryThreshV = 10
const noBatteryThreshV = 0.2

func getBatteryPercent(batteryConfig *goconfig.Battery, hvBat float32) float32 {
	if hvBat <= noBatteryThreshV {
		return 100
	}
	var voltConfig map[float32]float32
	if hvBat > limeBatteryThreshV {
		voltConfig = batteryConfig.Lime
	} else {
		voltConfig = batteryConfig.LiIon
	}

	var upper float32 = 0
	var lower float32 = 0

	for voltage := range voltConfig {
		lower = upper
		upper = voltage

		if hvBat >= lower && hvBat < upper {
			break
		}
		if hvBat <= lower && hvBat <= upper {
			//probably  have wrong battery config
			log.Printf("Could not find a matching voltage range in config for %v", hvBat)
			return voltConfig[upper]
		}
	}
	gradient := (voltConfig[upper] - voltConfig[lower]) / (upper - lower)
	batteryPercent := gradient*hvBat + voltConfig[lower] - gradient*lower
	return batteryPercent
}

func monitorVoltageLoop(a *attiny, config *goconfig.Config) {
	batteryConfig := goconfig.DefaultBattery()
	if err := config.Unmarshal(goconfig.BatteryKey, &batteryConfig); err != nil {
		return
	}
	err := keepLastLines("/var/log/battery-readings.csv", 500)
	if err != nil {
		log.Printf("Could not truncate /var/log/battery-readings.csv to 500 lines %v", err)
	}
	var batteryPercent float32 = -1.0
	startTime := time.Now()
	for {
		hvBat, err := a.readMainBattery()
		if err != nil {
			log.Fatal(err)
		}
		lvBat, err := a.readLVBattery()
		if err != nil {
			log.Fatal(err)
		}
		rtcBat, err := a.readRTCBattery()
		if err != nil {
			log.Fatal(err)
		}
		if time.Since(startTime) > time.Duration(24*time.Hour) {
			err := keepLastLines("/var/log/battery-readings.csv", 500)
			if err != nil {
				//not sure why it would error but should we keep trying...
				log.Printf("Could not truncate /var/log/battery-readings.csv to 500 lines %v", err)
			} else {
				startTime = time.Now()
			}
		}
		file, err := os.OpenFile("/var/log/battery-readings.csv", os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
		if err != nil {
			log.Fatal(err)
		}
		line := fmt.Sprintf("%s, %.2f, %.2f, %.2f", time.Now().Format("2006-01-02 15:04:05"), hvBat, lvBat, rtcBat)
		log.Println("Battery reading:", line)
		_, err = file.WriteString(line + "\n")
		file.Close()
		if err != nil {
			log.Fatal(err)
		}
		newPercent := getBatteryPercent(&batteryConfig, hvBat)
		if batteryPercent == -1 || batteryPercent-newPercent >= 10 {
			//log battery percent
			batteryPercent = newPercent
			eventclient.AddEvent(eventclient.Event{
				Timestamp: time.Now(),
				Type:      "rpiBattery",
				Details: map[string]interface{}{
					"battery": math.Round((float64(batteryPercent))),
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
	if stayOnUntil.Before(newTime) {
		stayOnUntil = newTime
		log.Println("Staying on until", stayOnUntil.Format(time.DateTime))
	}
	mu.Unlock()
	return nil
}

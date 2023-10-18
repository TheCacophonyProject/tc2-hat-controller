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
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	"github.com/TheCacophonyProject/go-config"
	arg "github.com/alexflint/go-arg"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/host/v3"
)

const (
	initialGracePeriod         = 20 * time.Minute
	saltCommandMaxWaitDuration = 30 * time.Minute
	saltCommandWaitDuration    = time.Minute
)

var (
	version = "<not set>"

	mu                 sync.Mutex
	stayOnUntil        = time.Now()
	saltCommandWaitEnd = time.Time{}
)

type Args struct {
	ConfigDir          string `arg:"-c,--config" help:"configuration folder"`
	SkipWait           bool   `arg:"-s,--skip-wait" help:"will not wait for the date to update"`
	Timestamps         bool   `arg:"-t,--timestamps" help:"include timestamps in log output"`
	SkipSystemShutdown bool   `arg:"--skip-system-shutdown" help:"don't shut down operating system when powering down"`
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
	// If no error then keep the background goroutines running.
	runtime.Goexit()
}

func runMain() error {
	args := procArgs()

	if !args.Timestamps {
		log.SetFlags(0)
	}

	log.Printf("running version: %s", version)

	conf, err := ParseConfig(args.ConfigDir)
	if err != nil {
		return err
	}

	_, err = host.Init()
	if err != nil {
		return err
	}
	bus, err := i2creg.Open("")
	if err != nil {
		return err
	}

	log.Println("Connecting to ATtiny.")
	attiny, err := connectToATtinyWithRetries(10, bus)
	if err != nil {
		return err
	}
	if err := startService(attiny); err != nil {
		return err
	}

	log.Println("Starting RTC service.")
	if err := attiny.updateConnectionState(); err != nil {
		return err
	}

	go monitorVoltageLoop(attiny)
	go checkATtinySignalLoop(attiny)

	log.Println("Connecting to RTC")
	rtc, err := InitPCF9564(bus)
	if err != nil {
		return err
	}
	if err := startRTCService(rtc); err != nil {
		return err
	}

	if err := rtc.SetSystemTime(); err != nil {
		log.Println(err)
	}

	if err := rtc.ClearAlarmFlag(); err != nil {
		return err
	}

	t, integrity, err := rtc.GetTime()
	if err != nil {
		return err
	}
	log.Println("RTC time:", t.Format(time.RFC3339))
	log.Println("RTC integrity:", integrity)

	attiny.readCameraState()
	log.Println(attiny.CameraState)

	if conf.OnWindow.NoWindow {
		log.Println("No Power On window, will stay powered on 24/7.")
		return nil
	}

	waitDuration := time.Duration(0)
	waitReason := ""
	if args.SkipWait {
		log.Println("Not waiting initial grace period.")
	} else {
		waitDuration = initialGracePeriod
		waitReason = fmt.Sprintf("Waiting initial grace period of %s", durToStr(waitDuration))
	}

	for {
		untilNextEnd := time.Until(conf.OnWindow.NextEnd())
		if conf.OnWindow.Active() && untilNextEnd > waitDuration {
			waitDuration = untilNextEnd
			waitReason = fmt.Sprintf("Waiting until end of POWERED ON window %s", durToStr(waitDuration))
		}

		stayOnUntilDuration := time.Until(stayOnUntil)
		if stayOnUntilDuration > waitDuration {
			waitDuration = stayOnUntilDuration
			waitReason = fmt.Sprintf("Camera has been requested to stay on for %s", durToStr(waitDuration))
		}

		if waitDuration < saltCommandWaitDuration && shouldStayOnForSalt() {
			waitDuration = saltCommandWaitDuration
			waitReason = fmt.Sprintf("Salt command is running, waiting %s", durToStr(waitDuration))
		}

		alarmTime := conf.OnWindow.NextStart()
		if time.Until(alarmTime) < 5*time.Minute {
			waitDuration = maxDuration(waitDuration, 5*time.Minute)
			waitReason = fmt.Sprintf("Waiting %s as the camera will be powering on soon anyway.", durToStr(waitDuration))
		}

		if waitDuration <= time.Duration(0) {
			log.Println("Alarm time:", alarmTime.Format(time.RFC3339))
			if err := rtc.SetAlarmTime(AlarmTimeFromTime(alarmTime)); err != nil {
				return err
			}
			if err := rtc.SetAlarmEnabled(true); err != nil {
				return err
			}
			attiny.poweringOff()
			log.Println("Shutting down.")
			time.Sleep(10 * time.Second)
			shutdown()
			time.Sleep(time.Second * 3)
			return nil

		}

		log.Println(waitReason)
		time.Sleep(waitDuration)
		waitDuration = time.Duration(0)
	}
}

func monitorVoltageLoop(a *attiny) {
	for {
		mainBat, err := a.readMainBattery()
		if err != nil {
			log.Fatal(err)
		}
		rtcBat, err := a.readRTCBattery()
		if err != nil {
			log.Fatal(err)
		}
		file, err := os.OpenFile("/var/log/battery-readings.csv", os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
		if err != nil {
			log.Fatal(err)
		}
		line := fmt.Sprintf("%s, %d, %d", time.Now().Format("2006-01-02 15:04:05"), mainBat, rtcBat)
		log.Println("Battery reading:", line)
		_, err = file.WriteString(line + "\n")
		file.Close()
		if err != nil {
			log.Fatal(err)
		}

		time.Sleep(2 * time.Minute)
	}
}

func checkATtinySignalLoop(a *attiny) {
	pinName := "GPIO16" //TODO add pin to config
	pin := gpioreg.ByName(pinName)
	pin.In(gpio.PullUp, gpio.FallingEdge)
	if pin == nil {
		log.Printf("Failed to find {%s}", pinName)
	}
	log.Println("Starting check ATtiny signal loop")
	for {
		pin.Read()
		if pin.Read() == gpio.High {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		log.Println("Signal from ATtiny")
		piCommands, err := a.readPiCommands(true)
		if err != nil {
			log.Println("Error reading pi commands:", err)
			continue
		}

		//TODO Fix bug causing this instead to be triggered twice, error is probably in ATtiny code
		log.Printf("Commands register: %x\n", piCommands)
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

		time.Sleep(time.Second)
	}
}

func isFlagSet(command, flag uint8) bool {
	return (command & flag) != 0
}

func enableWifi() {
	//TODO Make a better way to enable the hotspot/comms rather than just restart management-interface
	if err := exec.Command("systemctl", "restart", "managementd.service").Run(); err != nil {
		log.Println("Error restarting managementd.service:", err)
	}
}

func readAttinyErrors(a *attiny) {
	log.Println("Reading Attiny errors.")
	errorCodes, err := a.checkForErrorCodes(true)
	errorStrs := []string{}
	for _, err := range errorCodes {
		errorStrs = append(errorStrs, err.String())
	}
	if err != nil {
		log.Println("Error checking for errors on ATtiny:", err)
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
	}
	mu.Unlock()
	log.Println("staying on until", stayOnUntil.Format(time.UnixDate))
	return nil
}

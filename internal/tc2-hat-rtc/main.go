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

package rtc

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/alexflint/go-arg"
)

type Args struct {
	Service *subcommand `arg:"subcommand:service" help:"Start the dbus service."`
	SetTime string      `arg:"--set-time" help:"Set the time on the RTC. Format: 2006-01-02 15:04:05. Just used for debugging purposes."`
	Status  *subcommand `arg:"subcommand:status" help:"Get the status of the RTC."`
	logging.LogArgs
}

type subcommand struct {
}

var (
	log     = logging.NewLogger("info")
	version = "<not set>"
)

var defaultArgs = Args{}

func procArgs(input []string) (Args, error) {
	args := defaultArgs

	parser, err := arg.NewParser(arg.Config{}, &args)
	if err != nil {
		return Args{}, err
	}
	err = parser.Parse(input)
	if errors.Is(err, arg.ErrHelp) {
		parser.WriteHelp(os.Stdout)
		os.Exit(0)
	}
	if errors.Is(err, arg.ErrVersion) {
		fmt.Println(version)
		os.Exit(0)
	}
	return args, err
}

func Run(inputArgs []string, ver string) error {
	version = ver
	args, err := procArgs(inputArgs)
	if err != nil {
		return fmt.Errorf("failed to parse args: %v", err)
	}

	log = logging.NewLogger(args.LogLevel)

	log.Infof("Running version: %s", version)

	if args.Service != nil {
		if err := startService(); err != nil {
			return err
		}
		for {
			time.Sleep(time.Second)
		}
	} else if args.SetTime != "" {
		// TODO: Make this use the dbus service to set the time.
		rtc := &pcf8563{}
		newTime, err := time.Parse("2006-01-02 15:04:05", args.SetTime)
		if err != nil {
			return err
		}
		return rtc.SetTime(newTime)
	} else if args.Status != nil {
		// TODO: Make this use the dbus service to get the status.
		rtc := &pcf8563{}

		log.Println("Getting RTC status")
		alarmTime, err := rtc.ReadAlarmTime()
		if err != nil {
			log.Error("Error getting alarm time:", err)
		} else {
			log.Info("Alarm time:", alarmTime)
		}

		alarmEnabled, err := rtc.ReadAlarmEnabled()
		if err != nil {
			log.Error("Error getting alarm enabled:", err)
		} else {
			log.Info("Alarm enabled:", alarmEnabled)
		}

		alarmFlag, err := rtc.ReadAlarmFlag()
		if err != nil {
			log.Error("Error getting alarm flag:", err)
		} else {
			log.Info("Alarm flag:", alarmFlag)
		}

		rtcTime, integrity, err := rtc.GetTime()
		if err != nil {
			log.Error("Error getting RTC time/integrity:", err)
		} else {
			log.Info("RTC time:", rtcTime)
			log.Info("RTC integrity:", integrity)
		}
	}
	return nil
}

func startService() error {
	log.Debug("Connecting to RTC")
	rtc, err := InitPCF9564()
	if err != nil {
		return fmt.Errorf("failed to connect to RTC: %v", err)
	}

	log.Debug("Starting RTC DBus service.")
	if err := startRTCService(rtc); err != nil {
		return err
	}

	log.Debug("Setting RPi time from RTC.")
	if err := rtc.SetSystemTime(); err != nil {
		log.Println(err)
	}

	// Starting NTP sync loop. This is so when the NTP sync is done the RTC is set to the correct time.
	log.Debug("Starting NTP sync loop")
	go rtc.checkNtpSyncLoop()

	// Starting check ticking loop, this checks that the RTC is "ticking" properly.
	// We had a device where you could read/write times to the RTC, but the clock on the RTC was just staying the same after a write.
	log.Debug("Starting check ticking loop")
	go rtc.checkTickingLoop()

	return nil
}

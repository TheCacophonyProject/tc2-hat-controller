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
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
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

func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{}
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

	log.Printf("running version: %s", version)

	if args.Service != nil {
		if err := startService(); err != nil {
			return err
		}
		for {
			time.Sleep(time.Second)
		}
	} else if args.SetTime != "" {
		rtc := &pcf8563{}
		newTime, err := time.Parse("2006-01-02 15:04:05", args.SetTime)
		if err != nil {
			return err
		}
		return rtc.SetTime(newTime)
	} else if args.Status != nil {
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
	log.Println("Connecting to RTC")
	rtc, err := InitPCF9564()
	if err != nil {
		return err
	}
	log.Println("Starting RTC service.")
	if err := startRTCService(rtc); err != nil {
		return err
	}
	if err := rtc.SetSystemTime(); err != nil {
		log.Println(err)
	}

	// Read the time from the RTC then read it again after a few seconds to check that it is "ticking".
	checks := 0
	timeBetweenChecks := 10 * time.Second
	for {
		// Wait 10 seconds before doing another check.
		time.Sleep(10 * time.Second)

		// Get the time from the RTC 10 seconds apart.
		startTime, integrity, err := rtc.GetTime()
		if err != nil {
			log.Error("Error getting RTC time/integrity:", err)
			continue
		}
		if !integrity {
			log.Debug("RTC clock does't have integrity")
			continue
		}
		time.Sleep(timeBetweenChecks)
		endTime, integrity, err := rtc.GetTime()
		if err != nil {
			log.Error("Error getting RTC time/integrity:", err)
			continue
		}
		if !integrity {
			log.Debug("RTC clock does't have integrity")
			continue
		}

		// Compare the times, check if the RTC is ticking correctly.
		checks++
		diffFromExpected := (endTime.Sub(startTime) - timeBetweenChecks).Abs()
		if diffFromExpected > 2*time.Second {
			log.Debug("RTC clock is not ticking, or ticking incorrectly")

			if checks < 5 {
				// Let it try a few times as the RTC time might be getting updated, causing a false positive.
				continue
			}

			log.Errorf("RTC clock is not ticking, or ticking incorrectly times should be different by %s. Times: %s, %s", timeBetweenChecks, startTime, endTime)
			err := eventclient.AddEvent(eventclient.Event{
				Timestamp: time.Now(),
				Type:      "rtcNotTicking",
				Details: map[string]interface{}{
					"startTime":                  startTime.Format(time.DateTime),
					"endTime":                    endTime.Format(time.DateTime),
					"timeBetweenChecks":          timeBetweenChecks.String(),
					"timeDifferenceFromExpected": diffFromExpected.String(),
					eventclient.SeverityKey:      eventclient.SeverityError,
				},
			})
			if err != nil {
				log.Errorf("Error adding 'rtcNotTicking' event: %v", err)
			}
			break
		}
	}

	return nil
}

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
	"log"
	"time"

	"github.com/alexflint/go-arg"
)

type Args struct {
	Service *subcommand `arg:"subcommand:service" help:"Start the dbus service."`
}

type subcommand struct {
}

var (
	version = "<not set>"
)

func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{
		//ConfigDir: config.DefaultConfigDir,
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
	log.SetFlags(0)
	log.Printf("running version: %s", version)

	args := procArgs()
	if args.Service != nil {
		if err := startService(); err != nil {
			return err
		}
		for {
			time.Sleep(time.Second)
		}
	}

	/*
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
	*/

	/*
		t, integrity, err := rtc.GetTime()
		if err != nil {
			return err
		}
		log.Println("RTC time:", t.Format(time.RFC3339))
		log.Println("RTC integrity:", integrity)
		alarmTime, err := rtc.ReadAlarmTime()
		if err != nil {
			return err
		}
		log.Println("RTC alarm:", alarmTime)
	*/
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
	return nil
}

/*
	log.Println("Connecting to RTC")
	rtc, err := InitPCF9564()
	if err != nil {
		return err
	}

	for i := 2; i >= 0; i-- {
		if err := rtc.SetSystemTime(); err != nil {
			if i <= 0 {
				return err
			}
			log.Println(err)
			log.Printf("Retrying to set system time from RTC %d more time(s)...", i)
		} else {
			break
		}
	}
	return nil
}
*/

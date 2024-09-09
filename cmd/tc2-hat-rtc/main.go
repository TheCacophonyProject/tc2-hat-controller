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

	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/alexflint/go-arg"
)

type Args struct {
	Service *subcommand `arg:"subcommand:service" help:"Start the dbus service."`
	SetTime string      `arg:"--set-time" help:"Set the time on the RTC. Format: 2006-01-02 15:04:05. Just used for debugging purposes."`
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
	return nil
}

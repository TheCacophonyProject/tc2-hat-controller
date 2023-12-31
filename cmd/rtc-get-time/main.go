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

	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/host/v3"
)

var (
	version = "<not set>"
)

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err)
	}
}

func runMain() error {
	log.SetFlags(0)
	log.Printf("running version: %s", version)

	_, err := host.Init()
	if err != nil {
		return err
	}
	bus, err := i2creg.Open("")
	if err != nil {
		return err
	}

	log.Println("Connecting to RTC")
	rtc, err := InitPCF9564(bus)
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

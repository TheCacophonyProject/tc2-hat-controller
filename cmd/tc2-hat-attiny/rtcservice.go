/*
tc2-hat-controller - Communicates with ATtiny microcontroller
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
	"log"
	"time"

	"github.com/godbus/dbus"
)

const (
	rtcDbusName = "org.cacophony.RTC"
	rtcDbusPath = "/org/cacophony/RTC"
)

type rtcService struct {
	rtc *pcf8563
}

func startRTCService(a *pcf8563) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}
	reply, err := conn.RequestName(rtcDbusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("name already taken")
	}

	s := &rtcService{
		rtc: a,
	}
	conn.Export(s, rtcDbusPath, rtcDbusName)
	conn.Export(genIntrospectable(s), rtcDbusPath, "org.freedesktop.DBus.Introspectable")
	return nil
}

func (s rtcService) GetTime() (string, bool, *dbus.Error) {
	t, integrity, err := s.rtc.GetTime()
	if err != nil {
		log.Println(err)
		return "", false, makeDbusError(".GetTime", err)
	}
	return t.Format("2006-01-02T15:04:05Z07:00"), integrity, nil
}

func (s rtcService) SetTime(timeStr string) *dbus.Error {
	t, err := time.Parse("2006-01-02T15:04:05Z07:00", timeStr)
	if err != nil {
		log.Println(err)
		return makeDbusError(".SetTime", err)
	}
	err = s.rtc.SetTime(t)
	if err != nil {
		return makeDbusError(".SetTime", err)
	}
	return nil
}

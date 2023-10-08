/*
attiny-controller - Communicates with ATtiny microcontroller
Copyright (C) 2018, The Cacophony Project

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
	"github.com/godbus/dbus/introspect"
)

const (
	dbusName = "org.cacophony.ATtiny"
	dbusPath = "/org/cacophony/ATtiny"
)

type service struct {
	attiny *attiny
}

func startService(a *attiny) error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}
	reply, err := conn.RequestName(dbusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("name already taken")
	}

	s := &service{
		attiny: a,
	}
	conn.Export(s, dbusPath, dbusName)
	conn.Export(genIntrospectable(s), dbusPath, "org.freedesktop.DBus.Introspectable")
	return nil
}

func genIntrospectable(v interface{}) introspect.Introspectable {
	node := &introspect.Node{
		Interfaces: []introspect.Interface{{
			Name:    dbusName,
			Methods: introspect.Methods(v),
		}},
	}
	return introspect.NewIntrospectable(node)
}

// IsPresent returns whether or not an ATtiny was detected.
func (s service) IsPresent() (bool, *dbus.Error) {
	return s.attiny != nil, nil
}

// StayOnFor will delay turning off the raspberry pi for m minutes.
func (s service) StayOnFor(m int) *dbus.Error {
	err := setStayOnUntil(time.Now().Add(time.Duration(m) * time.Minute))
	if err != nil {
		return makeDbusError(".StayOnForError", err)
	}
	return nil
}

func (s service) UpdateWifiState() *dbus.Error {
	log.Println("Update wifi state requested.")
	if err := s.attiny.updateConnectionState(); err != nil {
		log.Println(err)
		return makeDbusError(".UpdateWifiState", err)
	}
	return nil
}

func makeDbusError(name string, err error) *dbus.Error {
	return &dbus.Error{
		Name: dbusName + name,
		Body: []interface{}{err.Error()},
	}
}

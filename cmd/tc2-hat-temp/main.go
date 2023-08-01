/*
SHT3x - Connecting to the SHT3x sensor.
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
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package main

import (
	"errors"
	"log"
	"time"

	arg "github.com/alexflint/go-arg"
	"github.com/snksoft/crc"
	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/host/v3"
)

const (
	i2cAddress      = 0x44
	maxTxAttempts   = 3
	txRetryInterval = time.Second

	// TODO make this configurable and check what are sensible values.
	// TODO make the service restart if the config changes.
	TEMP_MAX     = 50.0
	HUMIDITY_MAX = 50.0
	TEMP_MIN     = -10
)

var version = "No version provided"

type argSpec struct {
	I2cAddress uint16 `arg:"--address" help:"Address of MMC5603NJ sensor"`
}

func (argSpec) Version() string {
	return version
}

func procArgs() argSpec {
	args := argSpec{
		I2cAddress: i2cAddress,
	}
	arg.MustParse(&args)
	return args
}

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err.Error())
	}
}

func runMain() error {
	args := procArgs()
	log.SetFlags(0) // Removes default timestamp flag
	log.Printf("Running version: %s", version)

	if _, err := host.Init(); err != nil {
		return err
	}
	log.Println("Connecting to I2C bus.")
	bus, err := i2creg.Open("")
	if err != nil {
		return err
	}

	log.Printf("Connecting to humidity and temperature sensor at I2C address '0x%x'", args.I2cAddress)
	dev := &i2c.Dev{Bus: bus, Addr: args.I2cAddress}
	defer bus.Close()
	// Perform a dummy read to check if device is present.
	//TODO this check isn't working at the moment.
	//if err := dev.Tx([]byte{}, []byte{0}); err != nil {
	//	return fmt.Errorf("failed to connect to device: %w", err)
	//}

	log.Println("Connected.")
	s := SHT3x{
		dev: dev,
	}
	for {
		temp, humidity, err := s.MakeReading()
		if err != nil {
			return err
		}
		log.Printf("Temp: %.2f, Humidity: %.2f", temp, humidity)

		if temp > TEMP_MAX {
			log.Println("Temp too high!")
			//TODO do something..
		}
		if temp < TEMP_MIN {
			log.Println("Temp too low!")
			//TODO do something..
		}
		if humidity > HUMIDITY_MAX {
			log.Println("Humidity too high!")
			//TOOD do something..
		}

		time.Sleep(time.Minute)
	}
}

type SHT3x struct {
	dev *i2c.Dev
}

func (s *SHT3x) MakeReading() (float32, float32, error) {
	//Send make reading command
	if err := s.tx([]byte{0x24, 0x00}, nil); err != nil {
		return 0, 0, err
	}
	time.Sleep(30 * time.Millisecond)
	data := make([]byte, 6)
	if err := s.tx(nil, data); err != nil {
		return 0, 0, err
	}
	crcTable := crc.NewTable(&crc.Parameters{
		Width:      8,
		Polynomial: 0x31,
		ReflectIn:  false,
		ReflectOut: false,
		Init:       0xFF,
		FinalXor:   0x00,
	})

	// Check CRC
	if crcTable.CalculateCRC(data[0:2]) != uint64(data[2]) {
		return 0, 0, errors.New("crc for temp does not match")
	}
	if crcTable.CalculateCRC(data[3:5]) != uint64(data[5]) {
		return 0, 0, errors.New("crc for humidity does not match")
	}
	var tempRaw, humidityRaw uint16
	tempRaw |= uint16(data[0]) << 8
	tempRaw |= uint16(data[1])

	humidityRaw |= uint16(data[3]) << 8
	humidityRaw |= uint16(data[4])

	temp := float32(-45 + 175*float32(tempRaw)/float32(65535))
	humidity := float32(-45 + 175*float32(humidityRaw)/float32(65535))

	return temp, humidity, nil
}

func (m *SHT3x) tx(write, read []byte) error {
	attempts := 0
	for {
		err := m.dev.Tx(write, read)
		if err == nil {
			return nil
		}
		attempts++
		if attempts >= maxTxAttempts {
			return err
		}
		time.Sleep(txRetryInterval)
	}
}

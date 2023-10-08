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
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"

	"periph.io/x/conn/v3/i2c"
)

type Register uint8

const (
	typeReg Register = iota
	versionReg
	cameraStateReg
	cameraConnectionReg
	piCommandsReg
	triggerSleepReg
	piWakeUpReg
)

const (
	battery1Reg Register = iota + 0x10
	battery2Reg
	battery3Reg
	battery4Reg
	battery5Reg
	rtcBattery1Reg
	rtcBattery2Reg
)

const (
	regErrors1 Register = iota + 0x20
	regErrors2
	regErrors3
	regErrors4
	errorRegisters = 4
)

// PiCommandFlags
const (
	WriteCameraStateFlag = 1 << iota
	ReadErrorsFlag
	EnableWifiFlag
)

// Camera states.
type CameraState uint8

const (
	statePoweringOn CameraState = iota
	statePoweredOn
	statePoweringOff
	statePoweredOff
	stateConnectedToNetwork
	statePowerOnTimeout
)

type ConnectionState uint8

const (
	noConnection ConnectionState = iota
	connWifi
	hostingHotspot
)
const (
	// Version of firmware that this software works with.
	attinyFirmwareVersion = 2
	attinyI2CAddress      = 0x25
	hexFile               = "/etc/cacophony/attiny-firmware.hex"
	i2cTypeVal            = 0xCA

	// Parameters for transaction retries.
	maxTxAttempts   = 5
	txRetryInterval = time.Second

	wifiInterface = "wlan0" // If this is changed also change it in /_release/10-notify-attiny to match
)

func (s CameraState) String() string {
	switch s {
	case statePoweringOn:
		return "Powering On"
	case statePoweredOn:
		return "Powered On"
	case statePoweringOff:
		return "Powering Off"
	case statePoweredOff:
		return "Powered Off"
	case stateConnectedToNetwork:
		return "Connected to Network"
	case statePowerOnTimeout:
		return "Power On Timeout"
	default:
		log.Println("Unknown camera state:", int(s))
		return "Unknown"
	}
}

func (s ConnectionState) String() string {
	switch s {
	case noConnection:
		return "No Connection"
	case connWifi:
		return "Wifi"
	case hostingHotspot:
		return "Hosting Hotspot"
	default:
		log.Println("Unknown connection state:", int(s))
		return "Unknown"
	}
}

type ErrorCode uint8

const (
	POWER_ON_FAILED               ErrorCode = 0x02
	WATCHDOG_TIMEOUT              ErrorCode = 0x03
	INVALID_CAMERA_STATE          ErrorCode = 0x04
	WRITE_TO_READ_ONLY            ErrorCode = 0x05
	LOW_BATTERY_LEVEL_SET_TOO_LOW ErrorCode = 0x06
	INVALID_REG_ADDRESS           ErrorCode = 0x07
	INVALID_ERROR_CODE            ErrorCode = 0x08
	NO_PING_RESPONSE              ErrorCode = 0x09
	RTC_TIMEOUT                   ErrorCode = 0x0A
)

func (e ErrorCode) String() string {
	switch e {
	case POWER_ON_FAILED:
		return "POWER_ON_FAILED"
	case WATCHDOG_TIMEOUT:
		return "WATCHDOG_TIMEOUT"
	case INVALID_CAMERA_STATE:
		return "INVALID_CAMERA_STATE"
	case WRITE_TO_READ_ONLY:
		return "WRITE_TO_READ_ONLY"
	case LOW_BATTERY_LEVEL_SET_TOO_LOW:
		return "LOW_BATTERY_LEVEL_SET_TOO_LOW"
	case INVALID_REG_ADDRESS:
		return "INVALID_REG_ADDRESS"
	case INVALID_ERROR_CODE:
		return "INVALID_ERROR_CODE"
	case NO_PING_RESPONSE:
		return "NO_PING_RESPONSE"
	case RTC_TIMEOUT:
		return "RTC_TIMEOUT"
	default:
		return "UNKNOWN_ERROR_CODE"
	}
}

func attinyUPDIPing() error {
	command := []string{"pymcuprog", "-d", "attiny1616", "-t", "uart", "-u", "/dev/serial0", "ping"}
	return exec.Command(command[0], command[1:]...).Run()
}

func updateATtinyFirmware() error {
	log.Println("Pinging device.")
	if err := attinyUPDIPing(); err != nil {
		return err
	}
	time.Sleep(1 * time.Second)
	log.Println("Erasing device.") //TODO check if it erases EEPROM
	command := []string{"pymcuprog", "-d", "attiny1616", "-t", "uart", "-u", "/dev/serial0", "erase"}
	if err := exec.Command(command[0], command[1:]...).Run(); err != nil {
		return err
	}
	time.Sleep(1 * time.Second)
	log.Println("Writing new firmware.")
	command = []string{"pymcuprog", "-d", "attiny1616", "-t", "uart", "-u", "/dev/serial0", "write", "-f", hexFile}
	if err := exec.Command(command[0], command[1:]...).Run(); err != nil {
		return err
	}
	time.Sleep(1 * time.Second)
	return nil
}

// connectToATtinyWithRetries tries to connect to an ATtiny device a certain number
// of times. If it fails to connect it logs an error message, attempts to update the
// ATtiny firmware, and will then repeat the process (retries) times.
func connectToATtinyWithRetries(retries int, bus i2c.Bus) (*attiny, error) {
	attempt := 0
	for {
		attiny, err := connectToATtiny(bus)
		if err == nil {
			attiny.writeCameraState(statePoweredOn)
			return attiny, err
		}
		if attempt < retries {
			log.Printf("Failed to initialize attiny: %v, trying %d more times.\n", err, retries-attempt)
		} else {
			log.Println("Failed to connect to attiny.")
			return nil, err
		}
		if err := updateATtinyFirmware(); err != nil {
			log.Printf("Error updating firmware: %v\n.", err)
		}
		time.Sleep(time.Second)
		attempt++
	}
}

// connectToATtiny initializes the required drivers and connects to the ATtiny device
// over the I2C bus. It then verifies that the device is present on the I2C bus and
// that it responds correctly with the expected type byte and firmware version byte
// ensuring that it is running the correct firmware.
// If this fails, updating the ATtiny with updateATTinyFirmware() might resolve the issue.
func connectToATtiny(bus i2c.Bus) (*attiny, error) {
	// Check that a device is present on I2C bus at the attiny address.
	if err := bus.Tx(attinyI2CAddress, nil, nil); err != nil {
		return nil, fmt.Errorf("failed to find attiny device on i2c bus: %v", err)
	}

	// Check that the device at ATtiny address responds with the correct type byte.
	typeResponse := make([]byte, 1)
	if err := bus.Tx(attinyI2CAddress, []byte{byte(typeReg)}, typeResponse); err != nil {
		return nil, err
	}
	if typeResponse[0] != i2cTypeVal {
		return nil, fmt.Errorf("device responded with '0x%x' instead of the correct type byte '%x'", typeResponse[0], i2cTypeVal)
	}

	// Check that ATtiny is running the right version of firmware.
	versionResponse := make([]byte, 1)
	if err := bus.Tx(attinyI2CAddress, []byte{byte(versionReg)}, versionResponse); err != nil {
		return nil, err
	}
	if versionResponse[0] != attinyFirmwareVersion {
		return nil, fmt.Errorf("device version is %d instead of %d", versionResponse[0], attinyFirmwareVersion)
	}
	return &attiny{dev: &i2c.Dev{Bus: bus, Addr: attinyI2CAddress}, version: versionResponse[0]}, nil
}

type attiny struct {
	mu      sync.Mutex
	dev     *i2c.Dev
	version uint8

	wifiMu          sync.Mutex
	CameraState     CameraState
	ConnectionState ConnectionState
}

func (a *attiny) writeCameraState(newState CameraState) error {
	if err := a.writeRegister(cameraStateReg, uint8(newState), 3); err != nil {
		return err
	}
	currentState := a.CameraState
	if currentState != newState {
		log.Println("Changed camera state from ", currentState, " to ", newState)
	}
	a.CameraState = newState
	return nil
}

func (a *attiny) readPiCommands(clear bool) (uint8, error) {
	val, err := a.readRegister(piCommandsReg)
	if err != nil {
		return 0, err
	}
	if clear {
		return val, a.writeRegister(piCommandsReg, 0x00, -1)
	}
	return val, nil
}

// PowerOff asks the ATtiny to turn the system off.
func (a *attiny) poweringOff() error {
	log.Println("Asking ATtiny to power off raspberry pi")
	return a.writeRegister(triggerSleepReg, 0x01, -1)
}

func (a *attiny) writeConnectionState(newState ConnectionState) error {
	if err := a.writeRegister(cameraConnectionReg, uint8(newState), 3); err != nil {
		return err
	}
	if a.ConnectionState != newState {
		log.Println("Changed camera connection state from ", a.ConnectionState, " to ", newState)
	}
	a.ConnectionState = newState
	return nil
}

func (a *attiny) updateConnectionState() error {
	a.wifiMu.Lock()
	defer a.wifiMu.Unlock()

	ssid, t, err := checkWifiConnection(wifiInterface)
	if err != nil {
		return err
	}
	if ssid == "" {
		a.writeConnectionState(noConnection)
	} else if t == "AP" {
		a.writeConnectionState(hostingHotspot)
	} else if t == "managed" {
		a.writeConnectionState(connWifi)
	} else {
		log.Println("unknown state")
	}
	return nil

}

func (a *attiny) readCameraState() error {
	state, err := a.readRegister(cameraStateReg)
	if err != nil {
		return err
	}
	a.CameraState = CameraState(state)
	return nil
}

// TODO
func (a *attiny) readBattery(reg1, reg2 Register) (uint16, error) {
	// Write value to trigger reading of voltage.
	if err := a.writeRegister(reg1, 1<<7, -1); err != nil {
		return 0, err
	}
	// Wait for value to be reset indicating a new voltage reading.
	for i := 0; i < 5; i++ {
		time.Sleep(time.Millisecond * 200)
		val1, err := a.readRegister(reg1)
		if err != nil {
			return 0, err
		}
		if val1&(0x01<<7) == 0 {
			val2, err := a.readRegister(reg2)
			if err != nil {
				return 0, err
			}
			return (uint16(val1) << 8) | uint16(val2), nil
		}
	}
	return 0, fmt.Errorf("failed to read RTC battery voltage")
}

func (a *attiny) readMainBattery() (uint16, error) {
	//log.Println("Reading Main battery voltage.")
	return a.readBattery(battery1Reg, battery2Reg)
}

func (a *attiny) readRTCBattery() (uint16, error) {
	//log.Println("Reading RTC battery voltage.")
	return a.readBattery(rtcBattery1Reg, rtcBattery2Reg)
}

func (a *attiny) checkForErrorCodes(clearErrors bool) ([]ErrorCode, error) {
	errorIdCounter := 0
	errorCodes := []ErrorCode{}
	for i := 0; i < errorRegisters; i++ {
		errorReg := Register(int(regErrors1) + i)
		errors, err := a.readRegister(errorReg)
		if err != nil {
			return nil, err
		}
		for j := 0; j < 8; j++ {
			if errors&(1<<j) != 0 {
				errorCodes = append(errorCodes, ErrorCode(errorIdCounter))
			}
			errorIdCounter++
		}
		if clearErrors {
			a.writeRegister(errorReg, 0, -1)
		}
	}
	return errorCodes, nil
}

// writeRegister writes the specified data to the given register on the attiny device.
// If retries is 0 or above it will try to verify by reading the register back off the ATtiny.
// Set retries to -1 if you are not wanting to verify the write operation.
func (a *attiny) writeRegister(register Register, data uint8, retries int) error {
	write := []byte{byte(register), data}
	read := []byte{}
	if err := a.tx(write, read); err != nil {
		if retries <= 0 {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		return a.writeRegister(register, data, retries-1)
	}

	if retries <= -1 {
		return nil
	}

	registerVal, err := a.readRegister(register)
	if err != nil {
		if retries == 0 {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		return a.writeRegister(register, data, retries-1)
	}
	if registerVal != data {
		if retries == 0 {
			return fmt.Errorf("error writing 0x%x to register %d. Register is value 0x%x", data, register, registerVal)
		}
	}
	return nil
}

func (a *attiny) readRegister(register Register) (uint8, error) {
	write := []byte{byte(register)}
	read := make([]byte, 1)
	if err := a.tx(write, read); err != nil {
		return 0, err
	}
	return uint8(read[0]), nil
}

func (a *attiny) tx(write, read []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	attempts := 0
	for {
		err := a.dev.Tx(write, read)
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

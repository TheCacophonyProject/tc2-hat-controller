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
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	"github.com/TheCacophonyProject/rpi-net-manager/netmanagerclient"
	"github.com/TheCacophonyProject/tc2-hat-controller/eeprom"
	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"periph.io/x/conn/v3/gpio"
)

type Register uint8

const (
	typeReg Register = iota
	majorVersionReg
	cameraStateReg
	cameraConnectionReg
	piCommandsReg
	rp2040PiPowerCtrlReg
	auxTerminalReg
	tc2AgentReadyReg
	minorVersionReg
	flashErrorsReg
	clearErrorReg
	patchVersionReg
)

const (
	batteryCheckCtrlReg Register = iota + 0x10
	batteryLow1Reg
	batteryLow2Reg
	batteryLVDivVal1Reg
	batteryLVDivVal2Reg
	batteryHVDivVal1Reg
	batteryHVDivVal2Reg
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
	PowerDownFlag
	ToggleAuxTerminalFlag
)

type CameraState uint8

const (
	statePoweringOn CameraState = iota
	statePoweredOn
	statePoweringOff
	statePoweredOff
	statePowerOnTimeout
	stateRebooting
)

var (
	// These variables are set by environment variables. Travis will set them automatically from the .travis.yml.
	// The are needed for testing though so the values can be set as shown below.
	attinyMajorStr = "" // To set for testing run `export ATTINY_MAJOR=1`
	attinyMinorStr = "" // To set for testing run `export ATTINY_MINOR=0`
	attinyPatchStr = "" // To set for testing run `export ATTINY_PATCH=0`
	// TODO, check hash of the hex file before programming.
	attinyHexHash = "" // To set for testing run `export ATTINY_HASH=$(sha256sum _release/attiny-firmware.hex | cut -d ' ' -f 1)`
)

const (
	// Version of firmware that this software works with.
	attinyI2CAddress = 0x25
	hexFile          = "/etc/cacophony/attiny-firmware.hex"
	eepromData       = "/etc/cacophony/eeprom-data.json"
	i2cTypeVal       = 0xCA
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
	case statePowerOnTimeout:
		return "Power On Timeout"
	case stateRebooting:
		return "Rebooting"
	default:
		log.Println("Unknown camera state:", int(s))
		return "Unknown"
	}
}

type ConnectionState uint8

const (
	connStateWifiNoConnection ConnectionState = iota
	connStateWifiConnected
	connStateHotspot
	connStateWifiSettingUp
	connStateHotspotSettingUp
)

func (s ConnectionState) String() string {
	switch s {
	case connStateWifiNoConnection:
		return "WIFI, no connection"
	case connStateWifiConnected:
		return "Wifi, connected"
	case connStateHotspot:
		return "Hosting Hotspot"
	case connStateWifiSettingUp:
		return "Setting up WIFI"
	case connStateHotspotSettingUp:
		return "Setting up hotspot"
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
	CRC_ERROR                     ErrorCode = 0x0B
	BAD_I2C_LENGTH_SHORT          ErrorCode = 0x0C
	BAD_I2C_LENGTH_LONG           ErrorCode = 0x0D
	BAD_I2C                       ErrorCode = 0x0E
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
	case CRC_ERROR:
		return "CRC_ERROR"
	case BAD_I2C_LENGTH_LONG:
		return "BAD_I2C_LENGTH_LONG"
	case BAD_I2C_LENGTH_SHORT:
		return "BAD_I2C_LENGTH_SHORT"
	case BAD_I2C:
		return "BAD_I2C"
	default:
		return fmt.Sprintf("UNKNOWN_ERROR_CODE 0x%02X", uint8(e))
	}
}

func attinyUPDIPing() error {
	command := []string{"pymcuprog", "-d", "attiny1616", "-t", "uart", "-u", "/dev/serial0", "ping"}
	return exec.Command(command[0], command[1:]...).Run()
}

func updateATtinyFirmware() error {
	hash, err := calculateSHA256(hexFile)
	if err != nil {
		return err
	}
	if hash != attinyHexHash {
		return fmt.Errorf("hashes of hex file don't match: expecting '%s', got '%s'", attinyHexHash, hash)
	}

	if serialhelper.SerialInUseFromTerminal() {
		_, err := exec.Command("disable-aux-uart").CombinedOutput()
		if err != nil {
			log.Println("Error disabling aux uart:", err)
		}
		if serialhelper.SerialInUseFromTerminal() {
			return errors.New("failed to disable serial for terminal use")
		} else {
			log.Println("Need to restart to have serial affect take place.")
			time.Sleep(5 * time.Second)
			log.Println("Powering off")
			output, err := exec.Command("/sbin/reboot").CombinedOutput()
			if err != nil {
				return fmt.Errorf("power off failed: %v\n%s", err, output)
			}
			time.Sleep(1 * time.Minute)
			return nil
		}

	}

	serialFile, err := serialhelper.GetSerial(3, gpio.Low, gpio.Low, time.Second)
	if err != nil {
		return err
	}
	defer serialhelper.ReleaseSerial(serialFile)

	// tc2-agent should be stopped and restarted if the attiny is being programmed because the RP2040 will restart in the reprogramming process
	tc2AgentService := "tc2-agent.service"
	tc2Enabled, err := checkServiceStatus(tc2AgentService)
	if err != nil {
		return err
	}
	if tc2Enabled {
		log.Println("Stopping tc2-agent")
		if err := exec.Command("systemctl", "stop", tc2AgentService).Run(); err != nil {
			return err
		}
	}

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

	if tc2Enabled {
		log.Println("Starting tc2-agent")
		if err := exec.Command("systemctl", "start", tc2AgentService).Run(); err != nil {
			return err
		}
	}
	return nil
}

// connectToATtinyWithRetries tries to connect to an ATtiny device a certain number
// of times. If it fails to connect it logs an error message, attempts to update the
// ATtiny firmware, and will then repeat the process (retries) times.
func connectToATtinyWithRetries(retries int) (*attiny, error) {
	attempt := 0
	for {
		attiny, err := connectToATtiny()
		if err == nil {
			attiny.writeCameraState(statePoweredOn)
			attiny.writeAuxState()
			return attiny, err
		}
		if attempt < retries {
			log.Printf("Failed to initialize attiny: %v, trying %d more times.\n", err, retries-attempt)
		} else {
			log.Println("Failed to connect to attiny.")
			return nil, err
		}

		// Need to stop tc2-hat-comms as it will be using the UART pins that are needed to update the firmware.
		service := "tc2-hat-comms.service"
		tc2CommsRunning, err := isServiceRunning(service)
		if err != nil {
			return nil, err
		}
		if tc2CommsRunning {
			if err := manageService("stop", service); err != nil {
				return nil, err
			}
			defer func() {
				if err := manageService("start", service); err != nil {
					log.Println("Failed to start service:", err)
				}
			}()
		}

		err = updateATtinyFirmware()
		if err != nil {
			log.Printf("Error updating firmware: %v\n.", err)
		}
		eventclient.AddEvent(eventclient.Event{
			Timestamp: time.Now(),
			Type:      "programmingAttiny",
			Details: map[string]interface{}{
				"success": err == nil,
			},
		})
		time.Sleep(time.Second)
		attempt++
	}
}

func (a *attiny) writeAuxState() error {
	var regVal uint8 = 0x00
	if serialhelper.SerialInUseFromTerminal() {
		regVal = 0x01
	}
	return a.writeRegister(auxTerminalReg, regVal, 3)
}

// connectToATtiny initializes the required drivers and connects to the ATtiny device
// over the I2C bus. It then verifies that the device is present on the I2C bus and
// that it responds correctly with the expected type byte and firmware version byte
// ensuring that it is running the correct firmware.
// If this fails, updating the ATtiny with updateATTinyFirmware() might resolve the issue.
func connectToATtiny() (*attiny, error) {
	// Check that a device is present on I2C bus at the attiny address.

	if err := i2crequest.CheckAddress(attinyI2CAddress, 1000); err != nil {
		return nil, fmt.Errorf("failed to find attiny device on i2c bus: %v", err)
	}

	// Check that the device at ATtiny address responds with the correct type byte.
	a := &attiny{version: 1}
	typeRead, err := a.readRegister(typeReg)
	if err != nil {
		return nil, fmt.Errorf("error reading type register %s", err)
	}
	log.Printf("Type: 0x%X", typeRead)
	if typeRead != i2cTypeVal {
		return nil, fmt.Errorf("device responded with '0x%x' instead of the correct type byte '%x'", typeRead, i2cTypeVal)
	}

	// Check that ATtiny is running the right version of firmware.
	majorVersionResponse, err := a.readRegister(majorVersionReg)
	if err != nil {
		return nil, err
	}
	attinyMajor, err := strconv.ParseUint(attinyMajorStr, 10, 8)
	if err != nil {
		return nil, err
	}
	log.Printf("Major Version: %d", majorVersionResponse)
	if majorVersionResponse != uint8(attinyMajor) {
		return nil, fmt.Errorf("device major version is %d instead of %d", majorVersionResponse, attinyMajor)
	}

	minorVersionResponse, err := a.readRegister(minorVersionReg)
	if err != nil {
		return nil, err
	}
	attinyMinor, err := strconv.ParseUint(attinyMinorStr, 10, 8)
	if err != nil {
		return nil, err
	}
	log.Printf("Minor Version: %d", minorVersionResponse)
	if minorVersionResponse != uint8(attinyMinor) {
		return nil, fmt.Errorf("device minor version is %d instead of %d", minorVersionResponse, attinyMinor)
	}

	patchVersionResponse, err := a.readRegister(patchVersionReg)
	if err != nil {
		return nil, err
	}
	attinyPatch, err := strconv.ParseUint(attinyPatchStr, 10, 8)
	if err != nil {
		return nil, err
	}
	log.Printf("Patch Version: %d", patchVersionResponse)
	if patchVersionResponse != uint8(attinyPatch) {
		return nil, fmt.Errorf("device patch version is %d instead of %d", patchVersionResponse, attinyPatch)
	}

	return &attiny{version: majorVersionResponse}, nil
}

type attiny struct {
	version uint8

	wifiMu          sync.Mutex
	CameraState     CameraState
	ConnectionState ConnectionState
}

func (a *attiny) writeCameraState(newState CameraState) error {
	mu.Lock()
	defer mu.Unlock()
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
	if val&0x01 == 0x01 {
		a.writeCameraState(a.CameraState)
	}
	if clear {
		return val, a.writeRegister(piCommandsReg, 0x00, 2)
	}
	return val, nil
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

func (a *attiny) checkForConnectionStateUpdates() error {
	for {
		stateChan, done, err := netmanagerclient.GetStateChanges()
		defer close(done)
		if err != nil {
			return err
		}
		state, err := netmanagerclient.ReadState()
		if err != nil {
			return err
		}
		if err := a.setConnectionState(state); err != nil {
			log.Println(err)
		}
		for state := range stateChan {
			log.Println(time.Now().Format(time.TimeOnly), state)
			if err := a.setConnectionState(state); err != nil {
				log.Println(err)
			}
		}
		time.Sleep(5 * time.Second)
	}
}

func (a *attiny) setConnectionState(state netmanagerclient.NetworkState) error {
	a.wifiMu.Lock()
	defer a.wifiMu.Unlock()
	switch state {
	case netmanagerclient.NS_INIT:
		return a.writeConnectionState(connStateWifiNoConnection)
	case netmanagerclient.NS_WIFI_OFF:
		return a.writeConnectionState(connStateWifiNoConnection)
	case netmanagerclient.NS_WIFI_SETUP:
		return a.writeConnectionState(connStateWifiSettingUp)
	case netmanagerclient.NS_WIFI_SCANNING:
		return a.writeConnectionState(connStateWifiSettingUp)
	case netmanagerclient.NS_WIFI_CONNECTING:
		return a.writeConnectionState(connStateWifiSettingUp)
	case netmanagerclient.NS_WIFI_CONNECTED:
		return a.writeConnectionState(connStateWifiConnected)
	case netmanagerclient.NS_HOTSPOT_STARTING:
		return a.writeConnectionState(connStateHotspotSettingUp)
	case netmanagerclient.NS_HOTSPOT_RUNNING:
		return a.writeConnectionState(connStateHotspot)
	case netmanagerclient.NS_ERROR:
		return a.writeConnectionState(connStateWifiNoConnection) //TODO change this.
	default:
		return fmt.Errorf("unknown connection state: '%s'", string(state))
	}
}

func (a *attiny) readCameraState() error {
	mu.Lock()
	defer mu.Unlock()
	state, err := a.readRegister(cameraStateReg)
	if err != nil {
		return err
	}
	a.CameraState = CameraState(state)
	return nil
}

func (a *attiny) readBattery(reg1, reg2 Register) (uint16, uint16, error) {
	numReadings := 5
	readings := make([]uint16, numReadings)
	var max = uint16(0)
	var min = uint16(math.MaxUint16)
	for i := 0; i < numReadings; i++ {
		val, err := a.makeIndividualAnalogReading(reg1, reg2)
		if err != nil {
			return 0, 0, err
		}
		readings[i] = val
		if val > max {
			max = val
		}
		if val < min {
			min = val
		}
	}
	log.Debugf("Analog readings. Max: %d, Min: %d", max, min)
	diff := max - min
	acceptableDifference := uint16(50)
	if diff > acceptableDifference {
		err := fmt.Errorf("difference in max and min analog readings was %d, readings were %v", diff, readings)
		return 0, 0, err
	}

	sum := 0
	for i := 0; i < numReadings; i++ {
		sum += int(readings[i])
	}
	avg := sum / numReadings
	log.Debugf("Analog average: %d", avg)

	return uint16(avg), diff, nil
}

func (a *attiny) makeIndividualAnalogReading(reg1, reg2 Register) (uint16, error) {
	// Write to the 7th bit to trigger an analog reading.
	if err := a.writeRegister(reg1, 1<<7, -1); err != nil {
		return 0, err
	}

	// Wait for ATtiny to make the analog reading
	time.Sleep(time.Millisecond * 200)

	// Read the first register
	val1, err := a.readRegister(reg1)
	if err != nil {
		return 0, err
	}

	// Check if the 7th bit has been set back to 0, indicating the analog reading has been made.
	if (val1 & (0x01 << 7)) != 0 {
		err := fmt.Errorf("analog reading not made")
		log.Errorf("Error making analog reading: %v", err)
		return 0, err
	}

	// Read the second register
	val2, err := a.readRegister(reg2)
	if err != nil {
		return 0, err
	}

	// Combine the two register values
	reading := (uint16(val1) << 8) | uint16(val2)
	log.Debugf("Raw battery reading: %d", reading)
	return reading, nil
}

/*
 Voltage Divider Circuit Diagram

  V_bat
   |
  R1
   |-- V_out
  R2
   |
  GND

 V_bat = V_in * ((R1 + R2)/R2)
*/

func (a *attiny) readRTCBattery() (float32, error) {
	raw, _, err := a.readBattery(batteryLVDivVal1Reg, batteryLVDivVal2Reg)
	if err != nil {
		return 0, err
	}
	hardwareVersion, err := eeprom.GetPowerPCBVersion()
	if err != nil {
		return 0, err
	}
	return calculateBatteryVoltage(raw, versionStr(hardwareVersion), rtcResistorVals)
}

func (a *attiny) readLVBattery() (float32, error) {
	raw, _, err := a.readBattery(batteryLVDivVal1Reg, batteryLVDivVal2Reg)
	if err != nil {
		return 0, err
	}
	hardwareVersion, err := eeprom.GetPowerPCBVersion()
	if err != nil {
		return 0, err
	}
	return calculateBatteryVoltage(raw, versionStr(hardwareVersion), lvResistorVals)
}

func (a *attiny) readHVBattery() (float32, error) {
	raw, _, err := a.readBattery(batteryHVDivVal1Reg, batteryHVDivVal2Reg)
	if err != nil {
		return 0, err
	}
	hardwareVersion, err := eeprom.GetPowerPCBVersion()
	if err != nil {
		return 0, err
	}
	return calculateBatteryVoltage(raw, versionStr(hardwareVersion), hvResistorVals)
}

func calculateBatteryVoltage(raw uint16, pcbVersion versionStr, resistorVals []rVals) (float32, error) {
	vref, r1, r2, err := getResistorDividerValuesFromVersion(pcbVersion, resistorVals)
	if err != nil {
		return 0, err
	}
	v := float32(raw) * vref / 1023 // raw is from 0 to 1023, 0 at 0V and 1023 at Vref
	return v * (r1 + r2) / (r2), nil
}

var lvResistorVals = []rVals{
	{"0.1.4", 3.3, 2000, 560 + 33},
	{"0.1.5", 3.325, 2000, 680},
	{"0.7.0", 3.3, 2000, 470},
}

var hvResistorVals = []rVals{
	{"0.1.4", 3.3, 2000, 150 + 22},
	{"0.1.5", 3.325, 2000, 168},
	{"0.7.0", 3.3, 2000, 168},
}

var rtcResistorVals = []rVals{
	{"0.1.4", 3.3, 0, 1},
	{"0.1.5", 3.325, 0, 1},
	{"0.7.0", 3.3, 0, 1},
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
	}
	if clearErrors {
		if err := a.writeRegister(clearErrorReg, 0, 3); err != nil {
			return nil, err
		}
	}
	return errorCodes, nil
}

// writeRegister writes the specified data to the given register on the attiny device.
// If retries is 0 or above it will try to verify by reading the register back off the ATtiny.
// Set retries to -1 if you are not wanting to verify the write operation.
func (a *attiny) writeRegister(register Register, data uint8, retries int) error {
	write := []byte{byte(register), data}
	if err := crcTxWithRetry(write, nil); err != nil {
		if retries <= 0 {
			return err
		}
		time.Sleep(100 * time.Millisecond)
		return a.writeRegister(register, data, retries-1)
	}

	if retries <= -1 {
		return nil
	}

	// Verify the write operation by reading back the data
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
			return fmt.Errorf("error writing 0x%x to register %d. Register value is 0x%x", data, register, registerVal)
		}
		time.Sleep(100 * time.Millisecond)
		return a.writeRegister(register, data, retries-1)
	}
	return nil
}

func (a *attiny) readRegister(register Register) (uint8, error) {
	write := []byte{byte(register)}
	read := make([]byte, 1)
	if err := crcTxWithRetry(write, read); err != nil {
		return 0, err
	}

	return uint8(read[0]), nil
}

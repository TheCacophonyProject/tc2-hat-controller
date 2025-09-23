/*
SHT3x - Connecting to the AHT20 sensor.
Copyright (C) 2024, The Cacophony Project

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

package temp

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
	arg "github.com/alexflint/go-arg"
	"github.com/sigurn/crc8"
)

const (
	AHT20Address       = 0x38
	AHT20_BUSY         = 1 << 7
	AHT20_CALIBRATED   = 1 << 3
	AHT20_STATUS_REG   = 0x71
	maxTxAttempts      = 3
	txRetryInterval    = time.Second
	maxTempReadings    = 2000
	temperatureCSVFile = "/var/log/temperature.csv"
)

var version = "No version provided"

var log = logging.NewLogger("info")

type Args struct {
	LowTemp               int `arg:"--low-temp" help:"Temperatures below this will be reported as low"`
	MinTemp               int `arg:"--min-temp" help:"Temperatures below this will result in powering off the system //TODO"` //TODO
	HighTemp              int `arg:"--high-temp" help:"Temperatures above this will be reported as high"`
	MaxTemp               int `arg:"--max-temp" help:"Temperatures above this will result is powering off the system //TODO"` //TODO
	HighHumidity          int `arg:"--high-humidity" help:"Humidities above this will be reported as high"`
	MaxHumidity           int `arg:"--max-humidity" help:"Humidities above this will result in powering off the system //TODO"` //TODO
	SampleRateSeconds     int `arg:"--sample-rate" help:"Sample rate in seconds"`
	LogRateMinutes        int `arg:"--log-rate" help:"Log rate in minutes"`
	ReportIntervalMinutes int `arg:"--report-interval" help:"Max time between temperature reports in minutes"`
	logging.LogArgs
}

var defaultArgs = Args{
	LowTemp:               -10,
	MinTemp:               5,
	HighTemp:              50,
	MaxTemp:               80,
	HighHumidity:          70,
	MaxHumidity:           90,
	SampleRateSeconds:     60,
	LogRateMinutes:        5,
	ReportIntervalMinutes: 120,
}

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

	log.Info("Running version: ", version)

	lastReportTime := time.Time{}
	reportInterval := time.Duration(args.ReportIntervalMinutes) * time.Minute
	log.Debug("Setting report interval to ", reportInterval)

	lastLogTime := time.Time{}
	logRate := time.Duration(args.LogRateMinutes) * time.Minute
	log.Debug("Setting log rate to ", logRate)

	log.Info("Checking AHT20 calibration")
	if err := checkCalibration(); err != nil {
		return err
	}

	sampleRateDuration := time.Duration(args.SampleRateSeconds) * time.Second

	// Limit the number of temperatures readings
	if err := keepLastLines(temperatureCSVFile, maxTempReadings); err != nil {
		return err
	}
	trimTempFileTime := time.Now()

	for {
		if time.Since(trimTempFileTime) > 24*time.Hour {
			if err := keepLastLines(temperatureCSVFile, maxTempReadings); err != nil {
				return err
			}
			trimTempFileTime = time.Now()
		}

		temp, humidity, crc, err := makeReading()

		// Some sensors don't have a working CRC so in that case we make multiple readings quickly and check that they are about the same.
		if err == errBadCRC && crc == 0xFF {

			previousTemp := temp
			previousHumidity := humidity
			temp, humidity, crc, err = makeReading()
			if err == errBadCRC && crc == 0xFF {
				log.Debug("No CRC, checking with multiple readings")
				if math.Abs(float64(temp-previousTemp)) > 1 || math.Abs(float64(humidity-previousHumidity)) > 1 {
					log.Errorf("CRC failed, got 0X%X, temp: %.2f, humidity: %.2f", crc, temp, humidity)
					return errBadCRC
				}
				// Values are close enough to previous reading so likely to be correct.
			} else if err != nil {
				log.Errorf("CRC failed got 0X%X, temp: %.2f, humidity: %.2f", crc, temp, humidity)
				return err
			}
		} else if err != nil {
			return err
		}

		if time.Since(lastLogTime) > logRate {
			log.Infof("Temp: %.2f, Humidity: %.2f", temp, humidity)
			lastLogTime = time.Now()
		} else {
			log.Debugf("Temp: %.2f, Humidity: %.2f", temp, humidity)
		}

		file, err := os.OpenFile(temperatureCSVFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s, %.2f, %.2f", time.Now().Format("2006-01-02 15:04:05"), temp, humidity)
		_, err = file.WriteString(line + "\n")
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}

		reportType := ""

		if time.Since(lastReportTime) > reportInterval {
			reportType = "tempHumidity"
		}

		if temp > float32(args.HighTemp) {
			log.Info("Temp too high!")
			reportType = "tempTooHigh"
		}
		if temp < float32(args.LowTemp) {
			log.Info("Temp too low!")
			reportType = "tempTooLow"
		}
		if humidity > float32(args.HighHumidity) {
			log.Info("Humidity too high!")
			reportType = "humidityTooHigh"
		}

		if reportType != "" {
			log.Println("Reporting", reportType)
			err := eventclient.AddEvent(eventclient.Event{
				Timestamp: time.Now(),
				Type:      reportType,
				Details: map[string]interface{}{
					"temp":     temp,
					"humidity": humidity,
				},
			})
			if err != nil {
				return err
			}
			lastReportTime = time.Now()
		}

		time.Sleep(sampleRateDuration)
	}
}

func makeReading() (float32, float32, uint8, error) {
	var temp, humidity float32
	var crc uint8
	var err error
	for range 5 {
		temp, humidity, crc, err = makeReadingAttempt()
		if err == nil {
			break
		}
		log.Debug("Error in attempt for getting a reading: ", err)
		time.Sleep(5 * time.Second)
	}
	return temp, humidity, crc, err
}

func checkCalibration() error {
	var err error
	for range 5 {
		err = checkCalibrationAttempt()
		if err == nil {
			break
		}
		log.Debug("Error in attempt for checking calibration: ", err)
		time.Sleep(5 * time.Second)
	}
	return nil
}

// Check calibration just needs to be done once at startup.
func checkCalibrationAttempt() error {
	// Get status register.
	rawData, err := i2crequest.Tx(AHT20Address, []byte{AHT20_STATUS_REG}, 7, 3000)
	if err != nil {
		return err
	}

	// Check if it is calibrated from the status register.
	if (rawData[0] & AHT20_CALIBRATED) == AHT20_CALIBRATED {
		return nil
	}

	// Device is not calibrated. Trigger a reset/calibration by sending BE 08 00
	log.Debug("Deice is not calibrated. Triggering a manual calibration.")
	_, err = i2crequest.Tx(AHT20Address, []byte{0xBE, 0x08, 0x00}, 0, 3000)
	if err != nil {
		return err
	}

	// Wait 100ms until checking if it is calibrated again.
	time.Sleep(100 * time.Millisecond)

	// Get status register.
	rawData, err = i2crequest.Tx(AHT20Address, []byte{AHT20_STATUS_REG}, 7, 3000)
	if err != nil {
		return err
	}

	// Check if it is calibrated from the status register.
	if (rawData[0] & AHT20_CALIBRATED) == AHT20_CALIBRATED {
		return nil
	}

	return fmt.Errorf("calibration failed")
}

func makeReadingAttempt() (float32, float32, uint8, error) {
	// Trigger reading by sending AC 33 00
	_, err := i2crequest.Tx(AHT20Address, []byte{0xAC, 0x33, 0x00}, 0, 3000)
	if err != nil {
		return 0, 0, 0, err
	}

	// Wait for measurement to be made (datasheet says at least 75ms). Retry 3 times if not ready.
	ready := false
	var rawData []byte
	for range 3 {
		// Wait 100ms then check if the temperature reading is ready.
		time.Sleep(100 * time.Millisecond)
		rawData, err = i2crequest.Tx(AHT20Address, []byte{AHT20_STATUS_REG}, 7, 3000)
		if err != nil {
			return 0, 0, 0, err
		}

		// Check if the device is not busy
		if rawData[0]&AHT20_BUSY == 0x00 {
			ready = true
			break
		}
		log.Debug("Temperature reading is not yet ready")
	}
	if !ready {
		return 0, 0, 0, errors.New("temperature reading was not ready after 3 tries")
	}

	if len(rawData) != 7 {
		return 0, 0, 0, fmt.Errorf("reading length: %d", len(rawData))
	}

	humidityRaw := uint32(rawData[1])<<12 | uint32(rawData[2])<<4 | uint32(rawData[3]>>4)
	humidity := float32(humidityRaw) / float32(1<<20) * 100

	temperatureRaw := uint32(rawData[3]&0x0F)<<16 | uint32(rawData[4])<<8 | uint32(rawData[5])
	temp := float32(temperatureRaw)/float32(1<<20)*200 - 50

	crc := calculateCRC(rawData[:6])
	if rawData[6] != crc {
		return temp, humidity, rawData[6], errBadCRC
	}
	return temp, humidity, crc, nil
}

var errBadCRC = errors.New("bad crc")

func calculateCRC(data []byte) byte {
	crcTable := crc8.MakeTable(crc8.Params{
		Poly:   0x31, // Polynomial 1 + x^4 + x^5 + x^8
		Init:   0xFF,
		RefIn:  false,
		RefOut: false,
		XorOut: 0x00,
	})
	crc := crc8.Checksum(data, crcTable)
	return crc
}

// keepLastLines keeps the last `maxLines` lines of the specified file.
func keepLastLines(filePath string, maxLines int) error {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil
	}
	tmpFile := filepath.Join(os.TempDir(), filepath.Base(filePath)+".tmp")
	err := os.Remove(tmpFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	commands := []string{"sh", "-c", fmt.Sprintf("tail -n %d %s > %s", maxLines, filePath, tmpFile)}
	cmd := exec.Command(commands[0], commands[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("err running '%s', %v, %v", strings.Join(commands, " "), string(out), err)
	}
	return os.Rename(tmpFile, filePath)
}

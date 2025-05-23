package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
)

const (
	pcf8563Address = 0x51

	PCF8563_STAT2_REG = 0x01
	PCF8563_TI_TP     = 0x01 << 4 //TODO check what this means
	PCF8563_ALARM_AF  = 0x01 << 3
	PCF8563_TIMER_TF  = 0x01 << 2
	PCF8563_ALARM_AIE = 0x01 << 1
	PCF8563_TIMER_TIE = 0x01 << 0

	lastRtcWriteTimeFile = "/etc/cacophony/last-rtc-write-time"
)

type pcf8563 struct{}

func InitPCF9564() (*pcf8563, error) {
	// Check that a device is present on I2C bus at the PCF8563 address.
	device, err := i2crequest.CheckAddress(pcf8563Address, 1000)
	if err != nil {
		log.Errorf("Error checking for PCF8563 device: %v", err)
		time.Sleep(3 * time.Second)
		return InitPCF9564()
	}

	if device {
		log.Println("Found PCF8563 device on i2c bus")
	} else {
		return nil, fmt.Errorf("failed to find pcf8563 device on i2c bus")
	}

	rtc := &pcf8563{}
	go rtc.checkNtpSyncLoop()
	return rtc, nil
}

func (rtc *pcf8563) checkNtpSyncLoop() {
	hasSynced := false
	log.Println("Starting ntp sync loop")
	for {
		cmd := exec.Command("timedatectl", "status")
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("Error executing '%s' command: %v\n", strings.Join(cmd.Args, " "), err)
			log.Printf("Combined Output: %s\n", string(out))
			return
		}

		if strings.Contains(string(out), "synchronized: yes") {
			log.Println("Writing time to RTC")
			ntpTime := time.Now().UTC() // Close enough to NTP time as it has synchronized. //TODO find a way of checking how long ago the RPi did the NTP sync.

			// Write the time to the RTC
			if err := rtc.SetTime(ntpTime); err != nil {
				log.Println("Error setting time on RTC:", err)
			} else {
				hasSynced = true
			}
		}

		if hasSynced {
			time.Sleep(time.Hour)
		} else {
			time.Sleep(time.Second)
		}
	}
}

func checkRtcDrift(ntpTime time.Time, rtcTime time.Time, rtcIntegrity bool) error {
	// Get last time RTC was updated
	timeRaw, err := os.ReadFile(lastRtcWriteTimeFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("No previous RTC write time")
			return nil
		}
		log.Errorf("Error reading file: %v", err)
		return err
	}
	previousRtcWriteTime, err := time.Parse(time.DateTime, string(timeRaw))
	if err != nil {
		log.Error("Error parsing previous RTC write time:", err)
	}

	timeFromLastWrite := ntpTime.Sub(previousRtcWriteTime).Truncate(time.Second)
	rtcDriftSeconds := ntpTime.Sub(rtcTime).Truncate(time.Second).Seconds()

	// RTC only has a resolution of one second so we should reduce the
	// drift by 1 second when checking the drift over a time period
	// or else if the RTC is written to twice in a short period being off by 1 second
	// could account for a lot over a month.
	if rtcDriftSeconds >= 1 {
		rtcDriftSeconds -= 1
	} else if rtcDriftSeconds <= -1 {
		rtcDriftSeconds += 1
	}
	log.Println("RTC drift seconds:", rtcDriftSeconds)

	secondsInMonth := float64(60 * 60 * 24 * 30)
	rtcDriftSecondsPerMonth := rtcDriftSeconds * (secondsInMonth / timeFromLastWrite.Seconds())

	driftPerMonthError := secondsInMonth / timeFromLastWrite.Seconds()
	minimumDriftPerMonth := math.Max(0, math.Abs(rtcDriftSecondsPerMonth)-math.Abs(driftPerMonthError))

	if math.Abs(minimumDriftPerMonth) >= 10 {
		log.Println("Previous NTP write time:", previousRtcWriteTime)
		log.Println("Current rtc time:", rtcTime)
		log.Println("New NTP write time:", ntpTime)
		log.Println("Time from last write:", timeFromLastWrite)
		log.Println("RTC drift:", secondsToDuration(rtcDriftSeconds))
		log.Println("RTC drift per month:", secondsToDuration(rtcDriftSecondsPerMonth))
		log.Println("RTC drift per month error +-", secondsToDuration(driftPerMonthError))
		log.Println("RTC minimum drift per month:", secondsToDuration(minimumDriftPerMonth))

		event := eventclient.Event{
			Timestamp: time.Now(),
			Type:      "rtcNtpDrift",
			Details: map[string]interface{}{
				"rtcDriftSecondsPerMonth": int(rtcDriftSecondsPerMonth),
				"rtcDriftSeconds":         int(rtcDriftSeconds),
				"integrity":               rtcIntegrity,
			},
		}

		// Check if the minimum drift, accounting for the error is greater than 600 seconds
		if minimumDriftPerMonth > 600 { // TODO find a good value to have this as.
			log.Errorf("High RTC drift per month detected: %s", secondsToDuration(minimumDriftPerMonth))
			event.Type = "rtcNtpDriftHigh"
		}

		err := eventclient.AddEvent(event)
		if err != nil {
			log.Error("Error adding event:", err)
		}
	}
	return nil
}

func secondsToDuration(seconds float64) time.Duration {
	return time.Duration(time.Second * time.Duration(seconds))
}

// SetTime will set the time on the PCF8563 RTC
// This is first done by getting the current time on the RTC, the last time the RTC was updated
func (rtc *pcf8563) SetTime(newTime time.Time) error {
	rtcTime, integrity, err := rtc.GetTime()
	if !integrity {
		eventclient.AddEvent(eventclient.Event{
			Timestamp: time.Now(),
			Type:      "rtcIntegrityLost",
			Details: map[string]interface{}{
				"rtcTime": rtcTime.Format("2006-01-02 15:04:05"),
			},
		})
	}
	if err != nil {
		return err
	}
	if err := checkRtcDrift(newTime, rtcTime, integrity); err != nil {
		log.Println("Error checking RTC drift:", err)
	}

	newTime = newTime.UTC().Truncate(time.Second)
	err = writeBytes([]byte{
		0x02,
		toBCD(newTime.Second()),
		toBCD(newTime.Minute()),
		toBCD(newTime.Hour()),
		toBCD(newTime.Day()),
		toBCD(int(newTime.Weekday())),
		toBCD(int(newTime.Month())),
		toBCD(newTime.Year() % 100)}) // PCF8563 RTC is only 2-digit year
	if err != nil {
		return err
	}

	// Compare to check that time was written correctly.
	rtcTime, integrity, err = rtc.GetTime()
	if !integrity {
		return fmt.Errorf("rtc clock does't have integrity  RTC time is %s", rtcTime.Format("2006-01-02 15:04:05"))
	}
	if err != nil {
		return err
	}
	if rtcTime.Sub(newTime) > time.Second {
		return fmt.Errorf("error setting time. RTC time %s. Time it was set to %s", rtcTime.Format("2006-01-02 15:04:05"), newTime.Format("2006-01-02 15:04:05"))
	}

	// Save the time that was written to the RTC, this is used to calculate the drift of the RTC.
	f, err := os.OpenFile(lastRtcWriteTimeFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Println("Error creating file:", err)
	} else {
		_, _ = f.WriteString(newTime.Format(time.DateTime))
		_ = f.Close()
	}
	return nil
}

func (rtc *pcf8563) SetSystemTime() error {
	now, integrity, err := rtc.GetTime()
	if err != nil {
		return err
	}
	if !integrity {
		eventclient.AddEvent(eventclient.Event{
			Timestamp: time.Now(),
			Type:      "rtcIntegrityError",
		})
		return fmt.Errorf("rtc clock does't have integrity  RTC time is %s", now.Format(time.DateTime))
	}
	if now.Before(time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC)) {
		// TODO make wrong RTC time event to report to user.
		log.Println("RTC time is before 2023, not writing to system clock.")
		return nil
	}

	timeStr := now.Format(time.DateTime)

	before, err := time.Parse(time.DateTime, time.Now().Format(time.DateTime))
	if err != nil {
		return fmt.Errorf("error parsing system time before setting: %v", err)
	}

	log.Printf("Writing time to system clock (in UTC): %s", timeStr)
	cmd := exec.Command("date", "--utc", "--set", timeStr, "+%Y-%m-%d %H:%M:%S")
	log.Println(strings.Join(cmd.Args, " "))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error running: %s, err: %v, out: %s", cmd.Args, err, string(out))
	}

	after, err := time.Parse(time.DateTime, time.Now().Format(time.DateTime))
	if err != nil {
		return fmt.Errorf("error parsing system time before setting: %v", err)
	}

	log.Printf("System clock before writing: %s. System clock after writing: %s", before.Format(time.DateTime), after.Format(time.DateTime))
	log.Printf("System clock moved by: %s", after.Sub(before))
	return nil
}

func (rtc *pcf8563) GetTime() (time.Time, bool, error) {
	// Read the time from the RTC.
	data, err := readBytes(0x02, 7)
	if err != nil {
		return time.Time{}, false, err
	}

	// Convert the time from BCD to decimal, only reading the appropriate bits from the register.
	seconds := fromBCD(data[0] & 0x7F)
	minutes := fromBCD(data[1] & 0x7F)
	hours := fromBCD(data[2] & 0x3F)
	days := fromBCD(data[3] & 0x3F)
	months := fromBCD(data[5] & 0x1F)
	years := 2000 + fromBCD(data[6])
	integrity := data[0]&(1<<7) == 0

	return time.Date(years, time.Month(months), days, hours, minutes, seconds, 0, time.UTC), integrity, nil
}

type AlarmTime struct {
	Minute int // 0-59
	Hour   int // 0-23
	Day    int // Day of month
}

func (a AlarmTime) String() string {
	now := time.Now().UTC()
	alarmTime := time.Date(now.Year(), now.Month(), a.Day, a.Hour, a.Minute, 0, 0, time.UTC)
	return fmt.Sprintf("UTC: %s, Local: %s, Day: %02d time: %02d:%02d", alarmTime.Format("15:04:05"), alarmTime.Local().Format("15:04:05"), a.Day, a.Hour, a.Minute)
}

func AlarmTimeFromTime(t time.Time) AlarmTime {
	t = t.UTC()
	return AlarmTime{
		Minute: t.Minute(),
		Hour:   t.Hour(),
		Day:    t.Day(),
	}
}

// setAlarm sets the alarm on the PCF8563 RTC to the given time.
func (rtc *pcf8563) SetAlarmTime(a AlarmTime) error {
	log.Println("Setting alarm time to (UTC time):", a)
	err := writeBytes([]byte{
		0x09,
		toBCD(a.Minute),
		toBCD(a.Hour),
		toBCD(a.Day),
		0x80, // Disable day of weekday alarm register
	})
	if err != nil {
		log.Fatal(err)
	}

	// Compare to check that the alarm was written correctly.
	rtcAlarmTime, err := rtc.ReadAlarmTime()
	if err != nil {
		return err
	}
	if rtcAlarmTime != a {
		return fmt.Errorf("error setting alarm time. Alarm time %s. Time it was set to %s", rtcAlarmTime, a)
	}
	return nil
}

func (rtc *pcf8563) ReadAlarmTime() (AlarmTime, error) {
	b, err := readBytes(0x09, 4)
	if err != nil {
		return AlarmTime{}, err
	}

	minute := fromBCD(b[0] & 0x7F)
	hour := fromBCD(b[1] & 0x3F)
	day := fromBCD(b[2] & 0x3F)
	return AlarmTime{
		Minute: minute,
		Hour:   hour,
		Day:    day,
	}, nil
	//TODO Check that alarm flags are set properly
}

func (rtc *pcf8563) SetAlarmEnabled(alarmEnabled bool) error {
	alarmState, err := readByte(PCF8563_STAT2_REG)
	if err != nil {
		return err
	}

	alarmState |= PCF8563_ALARM_AF // Maintain the current state of the alarm flag (i.e., don't reset it).
	alarmState |= PCF8563_TIMER_TF // Maintain the current state of the timer flag (i.e., don't reset it).
	if alarmEnabled {
		alarmState |= PCF8563_ALARM_AIE // Alarm interrupt enabled
	} else {
		alarmState &= ^byte(PCF8563_ALARM_AIE) // Alarm interrupt disabled
	}

	if err := writeByte(PCF8563_STAT2_REG, byte(alarmState)); err != nil {
		return err
	}

	rtcAlarmEnabled, err := rtc.ReadAlarmEnabled()
	if err != nil {
		return err
	}
	if alarmEnabled != rtcAlarmEnabled {
		return fmt.Errorf("error setting alarm. Alarm %v. Alarm it was set to %v", alarmEnabled, rtcAlarmEnabled)
	}
	//TODO Check all other alarm register flags
	return nil
}

func (rtc *pcf8563) ReadAlarmEnabled() (bool, error) {
	state, err := readByte(PCF8563_STAT2_REG)
	if err != nil {
		return false, err
	}
	return state&PCF8563_ALARM_AIE == PCF8563_ALARM_AIE, nil
}

func (rtc *pcf8563) ReadAlarmFlag() (bool, error) {
	alarmState, err := readByte(PCF8563_STAT2_REG)
	//log.Printf("%08b\n", alarmState)
	if err != nil {
		return false, err
	}

	return alarmState&PCF8563_ALARM_AF == PCF8563_ALARM_AF, nil
}

func (rtc *pcf8563) ClearAlarmFlag() error {
	alarmState, err := readByte(PCF8563_STAT2_REG)
	if err != nil {
		return err
	}
	alarmState &= ^byte(PCF8563_ALARM_AF) // Clear alarm flag
	return writeByte(PCF8563_STAT2_REG, byte(alarmState))
}

// toBCD converts a decimal number to binary-coded decimal.
func toBCD(n int) byte {
	return byte(n)/10<<4 + byte(n)%10
}

// writeBytes writes the given bytes to the I2C device.
func writeBytes(data []byte) error {
	_, err := i2crequest.Tx(pcf8563Address, data, 0, 1000)
	return err
}

func fromBCD(b byte) int {
	return int(b&0x0F) + int(b>>4)*10
}

// readByte reads a byte from the I2C device from a given register.
func readByte(register byte) (byte, error) {
	response, err := i2crequest.Tx(pcf8563Address, []byte{register}, 1, 1000)
	if err != nil {
		return 0, err
	}
	return response[0], nil
}

// writeByte writes a byte to the I2C device at a given register.
func writeByte(register byte, data byte) error {
	return writeBytes([]byte{register, data})
}

// readBytes reads bytes from the I2C device starting from a given register.
func readBytes(register byte, length int) ([]byte, error) {
	return i2crequest.Tx(pcf8563Address, []byte{register}, length, 1000)
}

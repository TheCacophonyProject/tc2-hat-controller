package rtc

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
	// TODO: will error the first time as the I2C dbus interface is not yet up.
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
	return rtc, nil
}

func (rtc *pcf8563) checkNtpSyncLoop() {
	hasSynced := false
	for {
		cmd := exec.Command("timedatectl", "status")
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Errorf("Error executing '%s' command: %v\n", strings.Join(cmd.Args, " "), err)
			log.Errorf("Combined Output: %s\n", string(out))
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

func getLastWriteTime() (time.Time, error) {
	timeRaw, err := os.ReadFile(lastRtcWriteTimeFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("No previous RTC write time")
			return time.Time{}, nil
		}
		log.Errorf("Error reading file: %v", err)
		return time.Time{}, err
	}
	return time.Parse(time.DateTime, string(timeRaw))
}

func checkRtcDrift(ntpTime time.Time, rtcTime time.Time, rtcIntegrity bool) error {
	previousRtcWriteTime, err := getLastWriteTime()
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
			event.Details[eventclient.SeverityKey] = eventclient.SeverityError
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
		log.Error("Error checking RTC drift:", err)
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

	log.Debugf("Writing time to system clock (in UTC): %s", timeStr)
	cmd := exec.Command("date", "--utc", "--set", timeStr, "+%Y-%m-%d %H:%M:%S")
	log.Debugf("Running command: %s", strings.Join(cmd.Args, " "))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error running: %s, err: %v, out: %s", cmd.Args, err, string(out))
	}

	after, err := time.Parse(time.DateTime, time.Now().Format(time.DateTime))
	if err != nil {
		return fmt.Errorf("error parsing system time before setting: %v", err)
	}

	log.Infof("System clock before writing: %s. System clock after writing: %s", before.Format(time.DateTime), after.Format(time.DateTime))
	log.Infof("System clock moved by: %s", after.Sub(before))
	return nil
}

// GetTime will get the time from the PCF8563
// It will attempt 3 times to get the time.
func (rtc *pcf8563) GetTime() (time.Time, bool, error) {
	attempts := 3
	var t time.Time
	var integrity bool
	var err error
	for range attempts {
		t, integrity, err = rtc.getTimeFromMultipleReads()
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return t, integrity, err
}

// getTimeFromMultipleReads will read the time multiple times to check that the date is consistent across multiple reads.
func (rtc *pcf8563) getTimeFromMultipleReads() (time.Time, bool, error) {
	attempts := 3
	previousTime, previousIntegrity, err := rtc.getTime()
	if err != nil {
		return time.Time{}, false, err
	}
	for range attempts - 1 {
		currentTime, currentIntegrity, err := rtc.getTime()
		if err != nil {
			return time.Time{}, false, err
		}
		if previousIntegrity != currentIntegrity {
			return time.Time{}, false, fmt.Errorf("integrity mismatch")
		}

		if currentTime.Sub(previousTime).Abs() > 2*time.Second {
			return time.Time{}, false, fmt.Errorf("time mismatch")
		}
		previousIntegrity = currentIntegrity
		previousTime = currentTime
		time.Sleep(10 * time.Millisecond)
	}

	return previousTime, previousIntegrity, nil
}

// getTime will get the time from the PCF8563.
// We think it will sometimes get the wrong time so it is recommended to use GetTime()
func (rtc *pcf8563) getTime() (time.Time, bool, error) {
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

// checkTickingLoop will every 10 minutes, check that the time on the RTC is progressing (ticking) and is not frozen.
// This has been added as a check as we had a device where the RTC could be written and read from but the time
// was not progressing (after writing to it, if you read the time back after 10 seconds it was still the same time)
func (rtc *pcf8563) checkTickingLoop() {
	for {
		var event *eventclient.Event
		var err error
		// We want to check that it is not ticking properly at least 3 times.
		// With the potential for other processes to be reading/writing to the I2C clock we could
		// get some false positives so we will only report an error if we get it 3 times in a row.
		retries := 2
		for {
			event, err = rtc.checkTicking()
			if err == nil && event == nil {
				// No error or ticking event, break out of the loop.
				break
			}
			if retries == 0 {
				break
			}
			log.Warnf("Ticking error, will check %d more times before making an event.", retries)
			time.Sleep(5 * time.Second)
			retries--
		}
		if event != nil {
			log.Errorf("Issue with the RTC ticking, event: %+v", event)
			err := eventclient.AddEvent(*event)
			if err != nil {
				log.Errorf("Error adding event: %v", err)
			}
		}
		if err != nil {
			log.Errorf("Error checking RTC ticking: %v", err)
		}

		// Wait 10 minutes until running the next check.
		time.Sleep(10 * time.Minute)
	}
}

func (rtc *pcf8563) checkTicking() (*eventclient.Event, error) {
	log.Debug("Checking RTC is ticking")

	// Get the last time that the RTC was written to.
	// This is to be used so it can check that the RTC wasn't written to during this "ticking" check.
	lastWriteTimeAtStart, err := getLastWriteTime()
	if err != nil {
		return nil, err
	}

	// Get the first time from the RTC
	rtcStartTime, integrity, err := rtc.GetTime()
	if err != nil {
		return nil, fmt.Errorf("error getting RTC time/integrity: %v", err)
	}
	if !integrity {
		return nil, fmt.Errorf("RTC clock does't have integrity")
	}
	// Take start time at the end of getting the RTC time as if you take it from the start you are adding in potential
	// time of queuing the I2C requests. Once it has the time it will return very quickly so this should be fine.
	startTime := time.Now()

	time.Sleep(time.Second * 10)

	// Get the second time from the RTC
	rtcEndTime, integrity, err := rtc.GetTime()
	if err != nil {
		return nil, fmt.Errorf("error getting RTC time/integrity: %v", err)
	}
	if !integrity {
		return nil, fmt.Errorf("RTC clock does't have integrity")
	}
	// Even though we waited 10 seconds, it might have been a bit longer depending on how long the I2C requests take.
	// Using time.Since will use monotonic time so we don't need to worry about the system time being changed.
	timeBetweenChecks := time.Since(startTime)

	// Check if the time has changed as expected. Giving a possible error of 2 seconds (+- 1 second for reading each time).
	rtcTimeDifference := rtcEndTime.Sub(rtcStartTime)
	diffFromExpected := (rtcTimeDifference - timeBetweenChecks).Abs()
	if diffFromExpected <= 2*time.Second {
		log.Debugf("RTC is ticking within expected range. RTC time difference: %s, Time between checks: %s", rtcTimeDifference, timeBetweenChecks)
		return nil, nil
	}
	log.Warnf("RTC is not ticking as expected. Time between checks: %s, RTC time difference: %s", timeBetweenChecks, rtcTimeDifference)

	// Check that the RTC hasn't been written to during this check.
	lastWriteTimeAtEnd, err := getLastWriteTime()
	if err != nil {
		return nil, err
	}
	if lastWriteTimeAtStart != lastWriteTimeAtEnd {
		log.Info("RTC was written to during a ticking check. Will check again.")
		return rtc.checkTicking()
	}

	// Time difference is larger than expected. Return an event of the issue..
	event := &eventclient.Event{
		Timestamp: time.Now(),
		Type:      "rtcNotTicking",
		Details: map[string]interface{}{
			"startTime":                  rtcStartTime.Format(time.DateTime),
			"endTime":                    rtcEndTime.Format(time.DateTime),
			"timeBetweenChecks":          timeBetweenChecks.String(),
			"timeDifferenceFromExpected": diffFromExpected.String(),
			eventclient.SeverityKey:      eventclient.SeverityError,
		},
	}
	return event, nil
}

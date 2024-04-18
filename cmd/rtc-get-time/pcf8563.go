package main

import (
	"fmt"
	"log"
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
)

type pcf8563 struct {
}

func InitPCF9564() (*pcf8563, error) {
	// Check that a device is present on I2C bus at the PCF8563 address.
	if err := i2crequest.CheckAddress(pcf8563Address, 1000); err != nil {
		return nil, fmt.Errorf("failed to find pcf8563 device on i2c bus: %v", err)
	}
	rtc := &pcf8563{}
	return rtc, nil
}

func (rtc *pcf8563) SetTime(t time.Time) error {
	t = t.UTC()
	_, err := i2crequest.Tx(pcf8563Address,
		[]byte{
			0x02,
			toBCD(t.Second()),
			toBCD(t.Minute()),
			toBCD(t.Hour()),
			toBCD(t.Day()),
			toBCD(int(t.Weekday())),
			toBCD(int(t.Month())),
			toBCD(t.Year() % 100)},
		0, 1000)
	if err != nil {
		return err
	}

	// Compare to check that time was written correctly.
	rtcTime, integrity, err := rtc.GetTime()
	if !integrity {
		eventclient.AddEvent(eventclient.Event{
			Timestamp: time.Now(),
			Type:      "rtcIntegrityError",
		})
		return fmt.Errorf("rtc clock does't have integrity  RTC time is %s", rtcTime.Format("2006-01-02 15:04:05"))
	}
	if err != nil {
		return err
	}
	if rtcTime.Sub(t) > time.Second {
		return fmt.Errorf("error setting time. RTC time %s. Time it was set to %s", rtcTime.Format("2006-01-02 15:04:05"), t.Format("2006-01-02 15:04:05"))
	}
	return nil
}

func (rtc *pcf8563) SetSystemTime() error {
	now1, _, _ := rtc.GetTime()
	time.Sleep(time.Millisecond)
	now2, _, _ := rtc.GetTime()
	time.Sleep(time.Millisecond)
	now3, integrity, err := rtc.GetTime()
	if err != nil {
		return err
	}
	if !integrity {
		return fmt.Errorf("rtc clock does't have integrity  RTC time is %s", now3.Format("2006-01-02 15:04:05"))
	}

	if now3.Sub(now1) > time.Second || now3.Sub(now2) > time.Second {
		return fmt.Errorf("difference in times is more than 1 second when reading time multiple times")
	}

	now := now3

	if now.Before(time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC)) {
		// TODO make wrong RTC time event to report to user.
		log.Println("RTC time is before 2023, not writing to system clock.")
		return nil
	}
	timeStr := now.Format("2006-01-02 15:04:05")
	log.Printf("Writing time to system clock (in UTC): %s", timeStr)
	cmd := exec.Command("date", "--utc", "--set", timeStr, "+%Y-%m-%d %H:%M:%S")
	log.Println(strings.Join(cmd.Args, " "))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error running: %s, err: %v, out: %s", cmd.Args, err, string(out))
	}
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
	Minute int
	Hour   int
	Day    int
}

func (a AlarmTime) String() string {
	return fmt.Sprintf("day: %02d time: %02d:%02d", a.Day, a.Hour, a.Minute)
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

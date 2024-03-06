package main

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"periph.io/x/conn/v3/i2c"
)

func fromBCD(b byte) int {
	return int(b&0x0F) + int(b>>4)*10
}

// readBytes reads bytes from the I2C device starting from a given register.
func readBytes(dev *i2c.Dev, register byte, data []byte) error {
	return dev.Tx([]byte{register}, data)
}

// readByte reads a byte from the I2C device from a given register.
func readByte(dev *i2c.Dev, register byte) (byte, error) {
	data := make([]byte, 1)
	if err := dev.Tx([]byte{register}, data); err != nil {
		return 0, err
	}
	return data[0], nil
}

// writeByte writes a byte to the I2C device at a given register.
func writeByte(dev *i2c.Dev, register byte, data byte) error {
	_, err := dev.Write([]byte{register, data})
	return err
}

// toBCD converts a decimal number to binary-coded decimal.
func toBCD(n int) byte {
	return byte(n)/10<<4 + byte(n)%10
}

// writeBytes writes the given bytes to the I2C device.
func writeBytes(dev *i2c.Dev, data []byte) error {
	_, err := dev.Write(data)
	return err
}

func shutdown(a *attiny) error {
	err := a.writeCameraState(statePoweringOff) // Without setting the state to powering off the ATtiny will automatically reboot the RPi.
	if err != nil {
		return err
	}
	time.Sleep(5 * time.Second)
	log.Println("Powering off")
	output, err := exec.Command("/sbin/poweroff").CombinedOutput()
	if err != nil {
		return fmt.Errorf("power off failed: %v\n%s", err, output)
	}
	return nil
}

/*
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
*/

// shouldStayOnForSalt will check if a salt command is running via checking the output from `salt-call saltutil.running`
// If a device is being kept on for too long because of salt commands it will ignore the salt command check.
func shouldStayOnForSalt() bool {
	if saltCommandWaitEnd.IsZero() {
		saltCommandWaitEnd = time.Now().Add(saltCommandMaxWaitDuration)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stdout, err := exec.CommandContext(ctx, "salt-call", "--local", "saltutil.running").Output()
	if err != nil {
		log.Println(err)
		return false
	}

	strOut := string(stdout)
	if strings.Count(strOut, "\n") <= 2 { // If a salt command is running the output will have much more than 2 lines.
		return false
	}

	if time.Now().After(saltCommandWaitEnd) {
		log.Printf("waiting for salt command for too long (%v)", saltCommandMaxWaitDuration)
		log.Printf("salt command:\n%v", strOut)
		return false
	}
	log.Println("staying on for salt command to finish")
	return true
}

func durToStr(duration time.Duration) string {
	return duration.Truncate(time.Second).String()
}

func crcTxWithRetry(dev *i2c.Dev, write, read []byte) error {
	i2cMu.Lock()
	defer i2cMu.Unlock()

	attempts := 0
	for {
		err := crcTX(dev, write, read)
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

func crcTX(dev *i2c.Dev, write, read []byte) error {
	writeCRC := calculateCRC(write)
	writeWithCRC := append(write, byte(writeCRC>>8), byte(writeCRC&0xFF))
	var readWithCRC []byte
	if read != nil {
		readWithCRC = append(read, 0, 0) // Read with 2 extra bytes for the response CRC
	}

	if err := dev.Tx(writeWithCRC, readWithCRC); err != nil {
		return err
	}

	if read != nil {
		calculatedCRC := calculateCRC(readWithCRC[:len(readWithCRC)-2])
		receivedCRC := uint16(readWithCRC[len(readWithCRC)-2])<<8 | uint16(readWithCRC[len(readWithCRC)-1])
		if calculatedCRC != receivedCRC {

			return fmt.Errorf("CRC mismatch: received 0x%X, calculated 0x%X", receivedCRC, calculatedCRC)
		}
	}

	for i := 0; i < len(read); i++ {
		read[i] = readWithCRC[i]
	}

	return nil
}

func calculateCRC(data []byte) uint16 {
	var crc uint16 = 0x1D0F // Initial value
	for _, b := range data {
		crc ^= uint16(b) << 8 // Shift byte into MSB of 16bit CRC
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021 // Polynomial 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

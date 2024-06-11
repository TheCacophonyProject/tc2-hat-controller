package i2crequest

import (
	"fmt"

	"github.com/godbus/dbus"
)

const (
	dbusName = "org.cacophony.i2c"
	dbusPath = "/org/cacophony/i2c"
)

func Tx(address byte, write []byte, readLen, timeout int) ([]byte, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	obj := conn.Object(dbusName, dbusPath)

	var response []byte
	if err := obj.Call(dbusName+".Tx", 0, address, write, readLen, timeout).Store(&response); err != nil {
		return nil, err
	}

	return response, nil
}

func CheckAddress(address byte, timeout int) error {
	_, err := Tx(address, []byte{0x00}, 1, timeout)
	return err
}

func TxWithCRC(address byte, write []byte, readLen, timeout int) ([]byte, error) {
	writeCRC := CalculateCRC(write)
	writeWithCRC := append(write, byte(writeCRC>>8), byte(writeCRC&0xFF))

	if readLen != 0 {
		readLen += 2
	}
	response, err := Tx(address, writeWithCRC, readLen, timeout)
	if err != nil {
		return nil, err
	}
	if readLen > 0 {
		calculatedCRC := CalculateCRC(response[:len(response)-2])
		receivedCRC := uint16(response[len(response)-2])<<8 | uint16(response[len(response)-1])
		if calculatedCRC != receivedCRC {
			return nil, fmt.Errorf("CRC mismatch: received 0x%X, calculated 0x%X", receivedCRC, calculatedCRC)
		}
	}
	if readLen == 0 {
		return []byte{}, nil
	} else {
		return response[:len(response)-2], nil
	}
}

func CalculateCRC(data []byte) uint16 {
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

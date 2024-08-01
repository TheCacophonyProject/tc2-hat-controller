package eeprom

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
)

type EepromDataV1 struct {
	Version byte      `json:"version"`
	Major   byte      `json:"major"`
	Minor   byte      `json:"minor"`
	Patch   byte      `json:"patch"`
	ID      uint64    `json:"id"`
	Time    time.Time `json:"time"`
}

func readEEPROMV1FromChip() (*EepromDataV1, error) {
	// Length of data:
	// Magic: 1
	// Version: 1
	// HardwareVersion 3
	// ID: 8
	// Time: 4
	// CRC: 2
	eepromDataLength := 1 + 1 + 3 + 8 + 4 + 2

	pageLength := 16 // Can only read one page on the eeprom chip at a time
	data := []byte{}
	for i := 0; i < eepromDataLength; i += pageLength {
		readLen := min(pageLength, eepromDataLength-i)
		pageData, err := i2crequest.Tx(EEPROM_ADDRESS, []byte{byte(i)}, readLen, 1000)
		if err != nil {
			return nil, err
		}
		data = append(data, pageData...)
	}
	if len(data) != eepromDataLength {
		return nil, fmt.Errorf("expected %d bytes, got %d", eepromDataLength, len(data))
	}
	all0xFF := true
	for _, b := range data {
		if b != 0xFF {
			all0xFF = false
			break
		}
	}
	if all0xFF {
		return nil, errEepromEmptyError
	}

	calculatedCRC := i2crequest.CalculateCRC(data[:len(data)-2])
	receivedCRC := uint16(data[len(data)-2])<<8 | uint16(data[len(data)-1])

	if data[0] != EEPROM_FIRST_BYTE {
		return nil, fmt.Errorf("invalid first byte: %#02X, expecting %#02X", data[0], EEPROM_FIRST_BYTE)
	}
	data = data[1:] // Remove the first byte

	version := data[0]

	// Extract hardware version
	major := data[1]
	minor := data[2]
	patch := data[3]

	// Extract id
	id := binary.BigEndian.Uint64(data[4:12])

	// Extract timestamp
	timeBytes := data[12:16]
	timestamp := binary.BigEndian.Uint32(timeBytes)
	readTime := time.Unix(int64(timestamp), 0)
	readTime = readTime.Truncate(time.Second)

	e := &EepromDataV1{
		Version: version,
		Major:   major,
		Minor:   minor,
		Patch:   patch,
		ID:      id,
		Time:    readTime,
	}

	if calculatedCRC != receivedCRC {
		return e, errEepromCRCFail
	}
	return e, nil
}

func (e *EepromDataV1) WriteData() []byte {
	// Hardware version data
	hardwareVersionData := []byte{
		e.Major,
		e.Minor,
		e.Patch,
	}

	// ID data
	idBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idBytes, e.ID)

	// Current time as Unix timestamp (32-bit)
	currentTime := uint32(e.Time.Unix())
	timeBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(timeBytes, currentTime)

	// Combine all parts into a single byte slice. Set first byte as EEPROM_FIRST_BYTE
	dataToWrite := append([]byte{EEPROM_FIRST_BYTE}, e.Version)
	dataToWrite = append(dataToWrite, hardwareVersionData...)
	dataToWrite = append(dataToWrite, idBytes...)
	dataToWrite = append(dataToWrite, timeBytes...)
	crc := i2crequest.CalculateCRC(dataToWrite)
	dataToWrite = append(dataToWrite, byte(crc>>8), byte(crc&0xFF))

	return dataToWrite
}

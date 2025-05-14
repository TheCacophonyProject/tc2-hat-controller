package eeprom

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
)

type EepromDataV2 struct {
	Version       byte      `json:"version"`
	MainPCB       SemVer    `json:"mainPCB"`
	PowerPCB      SemVer    `json:"powerPCB"`
	MicrophonePCB SemVer    `json:"microphonePCB"`
	TouchPCB      SemVer    `json:"touchPCB"`
	ID            uint64    `json:"id"`
	Time          time.Time `json:"time"`
	AudioOnly     bool      `json:"audioOnly"`
}

func readEEPROMV2FromChip() (*EepromDataV2, error) {
	// Length of data:
	// Magic: 1
	// Version: 1
	// HardwareVersion 3 *4
	// Audio 1
	// ID: 8
	// Time: 4
	// CRC: 2
	eepromDataLength := 1 + 1 + 3*4 + 1 + 8 + 4 + 2

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
	mainPCB, err := SemVerFromBytes(data[1:4])
	if err != nil {
		return nil, err
	}
	powerPCB, err := SemVerFromBytes(data[4:7])
	if err != nil {
		return nil, err
	}
	touchPCB, err := SemVerFromBytes(data[7:10])
	if err != nil {
		return nil, err
	}
	micPCB, err := SemVerFromBytes(data[10:13])
	if err != nil {
		return nil, err
	}

	audioOnly := data[13] == 0x01

	// Extract id
	id := binary.BigEndian.Uint64(data[14:22])

	// Extract timestamp
	timeBytes := data[22:26]
	timestamp := binary.BigEndian.Uint32(timeBytes)
	readTime := time.Unix(int64(timestamp), 0)
	readTime = readTime.Truncate(time.Second)

	e := &EepromDataV2{
		Version:       version,
		MainPCB:       mainPCB,
		PowerPCB:      powerPCB,
		TouchPCB:      touchPCB,
		MicrophonePCB: micPCB,
		AudioOnly:     audioOnly,
		ID:            id,
		Time:          readTime,
	}

	if calculatedCRC != receivedCRC {
		log.Infof("Calculated CRC for EEPROM: %#04X, received CRC: %#04X", calculatedCRC, receivedCRC)
		return e, errEepromCRCFail
	}
	return e, nil
}

func (e *EepromDataV2) WriteData() []byte {
	// Hardware version data
	hardwareVersionData := []byte{
		e.MainPCB.Major,
		e.MainPCB.Minor,
		e.MainPCB.Patch,
		e.PowerPCB.Major,
		e.PowerPCB.Minor,
		e.PowerPCB.Patch,
		e.TouchPCB.Major,
		e.TouchPCB.Minor,
		e.TouchPCB.Patch,
		e.MicrophonePCB.Major,
		e.MicrophonePCB.Minor,
		e.MicrophonePCB.Patch,
	}

	audioOnlyVersionByte := byte(0)
	if e.AudioOnly {
		audioOnlyVersionByte = 1
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
	dataToWrite = append(dataToWrite, audioOnlyVersionByte)
	dataToWrite = append(dataToWrite, idBytes...)
	dataToWrite = append(dataToWrite, timeBytes...)
	crc := i2crequest.CalculateCRC(dataToWrite)
	dataToWrite = append(dataToWrite, byte(crc>>8), byte(crc&0xFF))

	return dataToWrite
}

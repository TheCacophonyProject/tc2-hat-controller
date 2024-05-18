package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
)

const (
	EEPROM_ADDRESS    = 0x50
	EEPROM_FIRST_BYTE = 0xCA
	EEPROM_FILE       = "/etc/cacophony/eeprom-data.json"
)

type eepromData struct {
	Major byte      `json:"major"`
	Minor byte      `json:"minor"`
	Patch byte      `json:"patch"`
	ID    uint64    `json:"id"`
	Time  time.Time `json:"time"`
}

// Retroactively add data to eeprom if it doesn't exist.
// This should be removed at a future point and the data should be written to the flash
// file when the camera is put together.
var defaultEEPROM = &eepromData{
	Major: 0,
	Minor: 1,
	Patch: 4,
	ID:    generateRandomID(),
	Time:  time.Now().Truncate(time.Second),
}

// Hardware version if no EEPROM chip is found.
// If no EEPROM chip is found then it is a earlier version of the PCB so we set it to 0.1.3
var noEEPROMChip = &eepromData{
	Major: 0,
	Minor: 1,
	Patch: 3,
	ID:    generateRandomID(),
	Time:  time.Now().Truncate(time.Second),
}

// generateRandomID generates a 64-bit random identifier
func generateRandomID() uint64 {
	var id [8]byte
	_, err := rand.Read(id[:])
	if err != nil {
		log.Fatal(err)
	}
	return binary.BigEndian.Uint64(id[:])
}

var errEepromEmptyError = errors.New("eeprom no data found")

// Things to test.
// No EEPROM chip found. No file exists.				    // Should make default eeprom data file.		// Done
// No EEPROM chip found. File exists. Wrong data.	  // Should error.													  // Done
// No EEPROM chip found. File exists. correct data. // Success.															    // Done
// EEPROM chip found, no data. No file exists.	    // Should write default to eeprom and file. // Done
// EEPROM chip found, no data. File exists.		    	// Should error.														// Done
// EEPROM chip found, data. No file exists.		    	// Should write file.												// Done
// EEPROM chip found, data. File exists.			    	// Success.																	// Done
// EEPROM chip found, wrong data.							    	// Should error.

func initEEPROM() error {
	// Clear EEPROM for testing
	/*
		a := []byte{
			0x00,
		}
		for i := 0; i < 16; i++ {
			a = append(a, 0xFF)
		}
		log.Println(i2crequest.Tx(EEPROM_ADDRESS, a, 0, 1000))
	*/

	_, err := os.Stat(EEPROM_FILE)
	if os.IsNotExist(err) {
		err := i2crequest.CheckAddress(EEPROM_ADDRESS, 1000)
		if err != nil {
			log.Println("EEPROM chip not found and EEPROM data file doesn't exist. Creating it with default values.")
			return writeEEPROMToFile(noEEPROMChip)
		}
		eeprom, err := getEepromDataFromChip()
		if err == errEepromEmptyError {
			log.Println("EEPROM data file doesn't exist. EEPROM chip found and empty. Creating it with default values.")
			if err := writeEEPROMToFile(defaultEEPROM); err != nil {
				return fmt.Errorf("failed to write eeprom data to file: %v", err)
			}
			return writeStateToEEPROM(defaultEEPROM)
		} else if err != nil {
			return fmt.Errorf("failed to get eeprom data from chip: %v", err)
		}

		log.Println("EEPROM data file doesn't exist. Creating it with the data from the EEPROM chip.")
		return writeEEPROMToFile(eeprom)
	}

	if err := i2crequest.CheckAddress(EEPROM_ADDRESS, 1000); err != nil {
		// No EEPROM chip found.
		eepromDataFromFile, err := readEEPROMFromFile()
		if err != nil {
			return fmt.Errorf("failed to read eeprom data from file: %v", err)
		}
		if eepromDataFromFile.Major == noEEPROMChip.Major &&
			eepromDataFromFile.Minor == noEEPROMChip.Minor &&
			eepromDataFromFile.Patch == noEEPROMChip.Patch {
			log.Println("EEPROM not found and eeprom data file is as expected")
			return nil
		} else {
			return fmt.Errorf("EEPROM not found and eeprom data file is not as expected")
		}
	}

	log.Info("Reading EEPROM data.")
	eepromFromChip, err := getEepromDataFromChip()
	if err != nil {
		return fmt.Errorf("failed to get eeprom data from chip: %v", err)
	}

	eepromFromFile, err := readEEPROMFromFile()
	if err != nil {
		return fmt.Errorf("failed to read eeprom data from file: %v", err)
	}

	if eepromFromChip.Equal(eepromFromFile) {
		log.Println("EEPROM data is up to date.")
		return nil
	}

	return fmt.Errorf("EEPROM data does not match what is saved to file. Not too sure what we should do here")
}

func (eeprom *eepromData) Equal(other *eepromData) bool {
	return eeprom.Major == other.Major &&
		eeprom.Minor == other.Minor &&
		eeprom.Patch == other.Patch &&
		eeprom.ID == other.ID &&
		eeprom.Time.Equal(other.Time)
}

func getEepromDataFromChip() (*eepromData, error) {
	data, err := i2crequest.Tx(EEPROM_ADDRESS, []byte{0x00}, 16, 1000)
	if err != nil {
		return nil, err
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

	if len(data) != 16 {
		return nil, fmt.Errorf("invalid data length: %d, should be 16", len(data))
	}
	if data[0] != EEPROM_FIRST_BYTE {
		return nil, fmt.Errorf("invalid first byte: %#02X, expecting %#02X", data[0], EEPROM_FIRST_BYTE)
	}
	data = data[1:] // Remove the first byte

	// Extract hardware version
	major := data[0]
	minor := data[1]
	patch := data[2]

	// Extract id
	id := binary.BigEndian.Uint64(data[3:11])

	// Extract timestamp
	timeBytes := data[11:15]
	timestamp := binary.BigEndian.Uint32(timeBytes)
	readTime := time.Unix(int64(timestamp), 0)
	readTime = readTime.Truncate(time.Second)

	return &eepromData{
		Major: major,
		Minor: minor,
		Patch: patch,
		ID:    id,
		Time:  readTime,
	}, nil
}

func writeStateToEEPROM(eeprom *eepromData) error {
	// Check that the device has a eeprom chip
	err := i2crequest.CheckAddress(EEPROM_ADDRESS, 1000)
	if err != nil {
		return err
	}

	// Hardware version data
	hardwareVersionData := []byte{
		eeprom.Major,
		eeprom.Minor,
		eeprom.Patch,
	}

	// ID data
	idBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idBytes, eeprom.ID)

	// Current time as Unix timestamp (32-bit)
	currentTime := uint32(eeprom.Time.Unix())
	timeBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(timeBytes, currentTime)

	// Combine all parts into a single byte slice. Set first byte as EEPROM_FIRST_BYTE
	dataToWrite := append([]byte{EEPROM_FIRST_BYTE}, hardwareVersionData...)
	dataToWrite = append(dataToWrite, idBytes...)
	dataToWrite = append(dataToWrite, timeBytes...)

	// Append the address of 0x00 to the start of the data
	_, err = i2crequest.Tx(EEPROM_ADDRESS, append([]byte{0x00}, dataToWrite...), 0, 1000)
	if err != nil {
		return err
	}
	log.Println("Data written to EEPROM", dataToWrite)

	// Check that the data has been written correctly
	readData, err := i2crequest.Tx(EEPROM_ADDRESS, []byte{0x00}, len(dataToWrite), 1000)
	if err != nil {
		return err
	}

	log.Println("Data read from EEPROM", readData)
	if !bytes.Equal(readData, dataToWrite) {
		return errors.New("data mismatch")
	}

	return nil
}

func writeEEPROMToFile(eeprom *eepromData) error {
	eeprom.Time = eeprom.Time.Truncate(time.Second)
	data, err := json.MarshalIndent(eeprom, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal eeprom data: %v", err)
	}

	err = os.WriteFile(EEPROM_FILE, data, 0644)
	if err != nil {
		return fmt.Errorf("failed to write eeprom data to file: %v", err)
	}

	return nil
}

func readEEPROMFromFile() (*eepromData, error) {
	data, err := os.ReadFile(EEPROM_FILE)
	if err != nil {
		return nil, fmt.Errorf("failed to read eeprom data from file: %v", err)
	}

	var eeprom eepromData
	err = json.Unmarshal(data, &eeprom)
	eeprom.Time = eeprom.Time.Truncate(time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal eeprom data: %v", err)
	}

	return &eeprom, nil
}

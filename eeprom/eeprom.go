package eeprom

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
)

const (
	EEPROM_ADDRESS    = 0x50
	EEPROM_FIRST_BYTE = 0xCA
	EEPROM_FILE       = "/etc/cacophony/eeprom-data.json"
)

// Hardware version if no EEPROM chip is found.
// If no EEPROM chip is found then it is a earlier version of the PCB so we set it to 0.1.4
var noEEPROMChipData = &EepromDataV1{
	Version: 1,
	Major:   0,
	Minor:   1,
	Patch:   4,
	ID:      GenerateRandomID(),
	Time:    time.Now().Truncate(time.Second),
}

var log = logging.NewLogger("info")

// GenerateRandomID generates a 64-bit random identifier
func GenerateRandomID() uint64 {
	var id [8]byte
	_, err := rand.Read(id[:])
	if err != nil {
		log.Fatal(err)
	}
	return binary.BigEndian.Uint64(id[:])
}

var errEepromEmptyError = errors.New("eeprom no data found")
var errEepromCRCFail = errors.New("eeprom CRC check failed")

// Things to test.
// No EEPROM chip found. File exists. Wrong data.	  // Should error.													  // Done
// No EEPROM chip found. File exists. correct data. // Success.															    // Done
// EEPROM chip found, no data. File exists.		    	// Should error.														// Done
// EEPROM chip found, data. No file exists.		    	// Should write file.												// Done
// EEPROM chip found, data. File exists.			    	// Success.																	// Done
// EEPROM chip found, wrong data.							    	// Should error.

func noEEPROMChip() bool {
	return i2crequest.CheckAddress(EEPROM_ADDRESS, 1000) != nil
}

func InitEEPROM() error {
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

	eepromDataVersion := byte(0)
	eepromData := interface{}(nil)
	var err error

	if noEEPROMChip() {
		// Some early versions of the camera don't have an EEPROM chip.
		eepromDataVersion = 1
		eepromData = noEEPROMChipData
	} else {
		// Check what version of data we have on the EEPROM chip.
		eepromDataVersion, err = getEEPROMDataVersion()
		if err != nil {
			return err
		}
		log.Println("EEPROM data version:", eepromDataVersion)

		// Read the EEPROM data from the chip, depending on the version.
		switch eepromDataVersion {
		case 0x01:
			eepromData, err = readEEPROMV1FromChip()
			if err != nil {
				return err
			}
		case 0x02:
			eepromData, err = readEEPROMV2FromChip()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown EEPROM data version: %d", eepromDataVersion)
		}
	}

	// If there is not a EEPROM file, write one and exit.
	_, err = os.Stat(EEPROM_FILE)
	if os.IsNotExist(err) {
		log.Println("EEPROM chip not found and EEPROM data file doesn't exist. Creating it with default values.")
		return writeEEPROMToFile(eepromData)
	}

	// Read EEPROM from file and check that it matches.
	eepromDataFromFile, err := readEEPROMFromFile()
	if err != nil {
		return err
	}

	// check if the data matches
	if reflect.DeepEqual(eepromData, eepromDataFromFile) {
		log.Println("EEPROM data matches file")
		return nil
	}
	log.Printf("%+v\n", eepromData)
	log.Printf("%+v\n", eepromDataFromFile)
	return fmt.Errorf("EEPROM data does not match what is saved to file. Not too sure what we should do here")
}

func getEEPROMDataVersion() (byte, error) {
	// Read first byte to check what version of eeprom data we have.
	data, err := i2crequest.Tx(EEPROM_ADDRESS, []byte{0x00}, 2, 1000)
	if err != nil {
		return 0xFF, err
	}
	if len(data) != 2 {
		return 0xFF, fmt.Errorf("expected 1 byte, got %d", len(data))
	}
	if data[0] != EEPROM_FIRST_BYTE {
		return 0xFF, fmt.Errorf("expecting first byte on EEPROM to be 0x%X, got 0x%X. Has hte EEPROM chip been programmed?", EEPROM_FIRST_BYTE, data[0])
	}
	return data[1], nil
}

func writeEEPROMToFile(eeprom interface{}) error {
	data := []byte{}
	var err error

	switch eeprom := eeprom.(type) {
	case *EepromDataV1:
		log.Println("Writing EEPROM V1 data to file")
		eeprom.Time = eeprom.Time.Truncate(time.Second)
		data, err = json.MarshalIndent(eeprom, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal eeprom data: %v", err)
		}
	case *EepromDataV2:
		log.Println("Writing EEPROM V2 data to file")
		eeprom.Time = eeprom.Time.Truncate(time.Second)
		data, err = json.MarshalIndent(eeprom, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal eeprom data: %v", err)
		}
	}

	err = os.WriteFile(EEPROM_FILE, data, 0644)
	if err != nil {
		return fmt.Errorf("failed to write eeprom data to file: %v", err)
	}
	return nil
}

func readEEPROMFromFile() (interface{}, error) {
	data, err := os.ReadFile(EEPROM_FILE)
	if err != nil {
		return nil, fmt.Errorf("failed to read eeprom data from file: %v", err)
	}

	// Unmarshal to this to get the version number
	type VersionStruct struct {
		Version byte `json:"version"`
	}
	versionStruct := VersionStruct{}
	err = json.Unmarshal(data, &versionStruct)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal eeprom data: %v", err)
	}

	target := interface{}(nil)
	switch versionStruct.Version {
	case 1:
		target = &EepromDataV1{}
	case 2:
		target = &EepromDataV2{}
	default:
		return nil, fmt.Errorf("unsupported eeprom version: %d", versionStruct.Version)
	}

	err = json.Unmarshal(data, &target)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal eeprom data: %v", err)
	}
	return target, nil
}

type SemVer struct {
	Major byte
	Minor byte
	Patch byte
}

func NewSemVer(versionStr string) (*SemVer, error) {
	parts := strings.Split(versionStr, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid version format")
	}

	major, err := strconv.Atoi(parts[0][1:]) // Skip the 'v'
	if err != nil {
		return nil, fmt.Errorf("invalid major version")
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid minor version")
	}

	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid patch version")
	}

	return &SemVer{
		Major: byte(major),
		Minor: byte(minor),
		Patch: byte(patch),
	}, nil
}

func (v *SemVer) ToBytes() [3]byte {
	return [3]byte{v.Major, v.Minor, v.Patch}
}

func SemVerFromBytes(data []byte) (SemVer, error) {
	if len(data) != 3 {
		return SemVer{}, fmt.Errorf("expected 3 bytes, got %d", len(data))
	}
	return SemVer{
		Major: data[0],
		Minor: data[1],
		Patch: data[2],
	}, nil
}

func (v *SemVer) String() string {
	return fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
}

func GetMainPCBVersion() (string, error) {
	eepromData, err := readEEPROMFromFile()
	if err != nil {
		return "", err
	}

	switch eepromData := eepromData.(type) {
	case *EepromDataV1:
		return fmt.Sprintf("v%d.%d.%d", eepromData.Major, eepromData.Minor, eepromData.Patch), nil
	case *EepromDataV2:
		return eepromData.MainPCB.String(), nil
	default:
		return "", fmt.Errorf("unknown eeprom data type")
	}
}

func GetPowerPCBVersion() (string, error) {
	eepromData, err := readEEPROMFromFile()
	if err != nil {
		return "", err
	}

	switch eepromData := eepromData.(type) {
	case *EepromDataV1:
		return fmt.Sprintf("v%d.%d.%d", eepromData.Major, eepromData.Minor, eepromData.Patch), nil
	case *EepromDataV2:
		return eepromData.PowerPCB.String(), nil
	default:
		return "", fmt.Errorf("unknown eeprom data type")
	}
}

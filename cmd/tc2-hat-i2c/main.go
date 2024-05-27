package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/eeprom"
	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
	"github.com/alexflint/go-arg"
	"github.com/sirupsen/logrus"
)

var version = "<not set>"
var log = logrus.New()

type Args struct {
	Write    *Write      `arg:"subcommand:write"   help:"Write to a register."`
	Read     *Read       `arg:"subcommand:read"    help:"Read from a register."`
	Service  *subcommand `arg:"subcommand:service" help:"Start the dbus service."`
	Find     *Find       `arg:"subcommand:find"    help:"Find i2c devices."`
	LogLevel string      `arg:"-l, --loglevel" default:"info" help:"Set the logging level (debug, info, warn, error)"`
}

type writeEEPROMCommand struct {
	HardwareVersion string `arg:"required" help:"The hardware version of the device you want to program"`
}

type subcommand struct {
}

type Find struct {
	Address string `arg:"required" help:"The address of the device you want to find, in hex (0xnn)"`
}

type Write struct {
	Address string `arg:"required" help:"The address you want to write to, in hex (0xnn)"`
	Reg     string `arg:"required" help:"The Register you want to write to, in hex (0xnn)"`
	Val     string `arg:"required" help:"The value you want to write, in hex (0xnn)"`
}

type Read struct {
	Address string `arg:"required" help:"The address you want to read from, in hex (0xnn)"`
	Reg     string `arg:"required" help:"The Register you want to read from, in hex (0xnn)"`
}

func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{
		//ConfigDir: config.DefaultConfigDir,
	}
	arg.MustParse(&args)
	return args
}

func setLogLevel(level string) {
	switch level {
	case "debug":
		log.SetLevel(logrus.DebugLevel)
	case "info":
		log.SetLevel(logrus.InfoLevel)
	case "warn":
		log.SetLevel(logrus.WarnLevel)
	case "error":
		log.SetLevel(logrus.ErrorLevel)
	default:
		log.SetLevel(logrus.InfoLevel)
		log.Warn("Unknown log level, defaulting to info")
	}
}

// customFormatter defines a new logrus formatter.
type customFormatter struct{}

// Format builds the log message string from the log entry.
func (f *customFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	// Create a custom log message format here.
	return []byte(fmt.Sprintf("[%s] %s\n", strings.ToUpper(entry.Level.String()), entry.Message)), nil
}

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err)
	}
}

func runMain() error {
	log.SetFormatter(new(customFormatter))
	args := procArgs()
	setLogLevel(args.LogLevel)

	log.Infof("Running version: %s", version)

	if args.Write != nil {
		return write(args.Write)
	}
	if args.Read != nil {
		return read(args.Read)
	}
	if args.Find != nil {
		return find(args.Find)
	}

	if args.Service != nil {
		if err := startService(); err != nil {
			return err
		}

		if err := eeprom.InitEEPROM(); err != nil {
			log.Error(err)
		}

		for {
			time.Sleep(time.Second) // Sleep to prevent spinning
		}
	}

	return nil
}

func writeEEPROM(args *writeEEPROMCommand) error {

	parts := strings.Split(args.HardwareVersion, ".")
	if len(parts) != 3 {
		return fmt.Errorf("invalid hardware version '%s'", args.HardwareVersion)
	}

	major, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return err
	}
	minor, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return err
	}
	patch, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return err
	}

	eepromData := &eeprom.EepromData{
		Version: 1,
		Major:   byte(major),
		Minor:   byte(minor),
		Patch:   byte(patch),
		ID:      eeprom.GenerateRandomID(),
		Time:    time.Now().Truncate(time.Second),
	}

	log.Printf("Writing EEPROM data: %+v", eepromData)
	eeprom.WriteStateToEEPROM(eepromData)

	log.Println("EEPROM data written to file.")
	return nil
}

func find(find *Find) error {
	address, err := hexStringToByte(find.Address)
	if err != nil {
		return err
	}

	log.Printf("Finding address 0x%X", address)
	err = i2crequest.CheckAddress(address, 1000)
	if err != nil {
		return errors.New("i2c device not found")
	}
	return nil
}

func read(read *Read) error {
	write, err := hexStringToByte(read.Reg)
	if err != nil {
		return err
	}

	address, err := hexStringToByte(read.Address)
	if err != nil {
		return err
	}

	log.Printf("Reading register 0x%X", write)
	var response []byte
	if address == 0x25 { // Add CRC for attiny at address 0x25
		response, err = i2crequest.TxWithCRC(address, []byte{write}, 1, 1000)
	} else {
		response, err = i2crequest.Tx(address, []byte{write}, 1, 1000)
	}
	if err != nil {
		return err
	}
	log.Println(response)
	return nil
}

func write(args *Write) error {
	reg, err := hexStringToByte(args.Reg)
	if err != nil {
		return err
	}

	val, err := hexStringToByte(args.Val)
	if err != nil {
		return err
	}
	write := []byte{reg, val}

	address, err := hexStringToByte(args.Address)
	if err != nil {
		return err
	}

	log.Printf("Writing 0x%X to register 0x%X", write, write)
	if address == 0x25 { // Add CRC for attiny at address 0x25
		_, err = i2crequest.TxWithCRC(address, write, 0, 1000)
	} else {
		_, err = i2crequest.Tx(address, write, 0, 1000)
	}
	if err != nil {
		return err
	}
	return nil
}

func hexStringToByte(hexStr string) (byte, error) {
	if len(hexStr) != 4 {
		return 0, fmt.Errorf("invalid hex string length: %d", len(hexStr))
	}
	if !strings.HasPrefix(hexStr, "0x") {
		return 0, fmt.Errorf("invalid hex string prefix, should be '0x': %s", hexStr)
	}
	val, err := strconv.ParseUint(hexStr[2:], 16, 8) // 16 for base, 8 for bit size
	if err != nil {
		return 0, err
	}
	return byte(val), nil
}

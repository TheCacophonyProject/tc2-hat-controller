package i2c

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/TheCacophonyProject/tc2-hat-controller/eeprom"
	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
	"github.com/alexflint/go-arg"
)

var version = "<not set>"
var log = logging.NewLogger("info")

type Args struct {
	Write    *Write      `arg:"subcommand:write"   help:"Write to a register."`
	Read     *Read       `arg:"subcommand:read"    help:"Read from a register."`
	Service  *subcommand `arg:"subcommand:service" help:"Start the dbus service."`
	Find     *Find       `arg:"subcommand:find"    help:"Find i2c devices."`
	EEPROM   *subcommand `arg:"subcommand:eeprom"  help:"Run EEPROM check."`
	LogLevel string      `arg:"-l, --log-level" default:"info" help:"Set the logging level (debug, info, warn, error)"`
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

var defaultArgs = Args{}

func procArgs(input []string) (Args, error) {
	args := defaultArgs

	parser, err := arg.NewParser(arg.Config{}, &args)
	if err != nil {
		return Args{}, err
	}
	err = parser.Parse(input)
	if errors.Is(err, arg.ErrHelp) {
		parser.WriteHelp(os.Stdout)
		os.Exit(0)
	}
	if errors.Is(err, arg.ErrVersion) {
		fmt.Println(version)
		os.Exit(0)
	}
	return args, err
}

func Run(inputArgs []string, ver string) error {
	version = ver
	args, err := procArgs(inputArgs)
	if err != nil {
		return fmt.Errorf("failed to parse args: %v", err)
	}
	log = logging.NewLogger(args.LogLevel)

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
	if args.EEPROM != nil {
		if err := eeprom.InitEEPROM(); err != nil {
			log.Error(err)
		}
	}

	return nil
}

func find(find *Find) error {
	address, err := hexStringToByte(find.Address)
	if err != nil {
		return err
	}

	log.Printf("Finding address 0x%X", address)
	found, err := i2crequest.CheckAddress(address, 1000)
	if err != nil {
		log.Errorf("Error checking for device: %v", err)
	}
	if found {
		log.Printf("Found device at address 0x%X", address)
	} else {
		log.Printf("Did not find device at address 0x%X", address)
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

	log.Printf("Writing 0x%X to register 0x%X", reg, val)
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

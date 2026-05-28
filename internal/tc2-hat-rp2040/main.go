package rp2040

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/alexflint/go-arg"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

var (
	version = "<not set>"
	log     = logging.NewLogger("info")
)

type Args struct {
	ELF         string `arg:"--elf" help:".elf file to program the RP2040 with."`
	RunPin      string `arg:"--run-pin" help:"Run GPIO pin for the RP2040."`
	BootModePin string `arg:"--boot-mode-pin" help:"Boot mode GPIO pin for the RP2040."`
	EraseFlash  bool   `arg:"--erase-flash" help:"Upload the program that will erase the flash on the RP2040"`
	logging.LogArgs
}

const (
	openOCDNotFoundMessage = `'openocd' was not found. Can be installed using apt 'sudo apt install openocd' or 
following section 5.1 at https://datasheets.raspberrypi.com/pico/getting-started-with-pico.pdf.
If installed using apt then use the config file '/etc/cacophony/raspberrypi-swd.cfg' as 
'interface/raspberrypi-swd.cfg' is not available with the current version provided by apt.`
	eraseFlashFirmware = "/etc/cacophony/rp2040-firmware-erase-flash.elf"
	eraseFlashHash     = "/etc/cacophony/rp2040-firmware-erase-flash.sha256"
)

var defaultArgs = Args{
	RunPin:      "GPIO23",
	BootModePin: "GPIO5",
}

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

func verifyFileHash(filePath, hashFilePath string) error {
	expectedHashBytes, err := os.ReadFile(hashFilePath)
	if err != nil {
		return fmt.Errorf("failed to read hash file: %v", err)
	}
	expectedHash := strings.TrimSpace(string(expectedHashBytes))

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("failed to hash file: %v", err)
	}
	actualHash := fmt.Sprintf("%x", h.Sum(nil))

	if actualHash != expectedHash {
		return fmt.Errorf("hash mismatch: expected %s, got %s", expectedHash, actualHash)
	}
	return nil
}

func Run(inputArgs []string, ver string) error {
	version = ver
	args, err := procArgs(inputArgs)
	if err != nil {
		return fmt.Errorf("failed to parse args: %v", err)
	}
	log = logging.NewLogger(args.LogLevel)

	log.Printf("Running version: %s", version)

	if args.ELF != "" && args.EraseFlash {
		return fmt.Errorf("must specify either --elf or --erase-flash, not both")
	}

	// Check if openocd is installed and ELF file exists
	if args.ELF != "" || args.EraseFlash {
		if args.ELF != "" {
			if _, err := os.Stat(args.ELF); err != nil {
				return fmt.Errorf("elf file not found: %v", err)
			}
		}
		if args.EraseFlash {
			if _, err := os.Stat(eraseFlashFirmware); err != nil {
				return fmt.Errorf("erase flash firmware not found: %v", err)
			}
			if err := verifyFileHash(eraseFlashFirmware, eraseFlashHash); err != nil {
				return fmt.Errorf("erase flash firmware hash check failed: %v", err)
			}
			log.Println("Erase flash firmware hash verified.")
		}
		cmd := exec.Command("openocd", "--version")
		if err := cmd.Run(); err != nil {
			log.Println(openOCDNotFoundMessage)
			return errors.New("openocd not found")
		}
	}

	if _, err := host.Init(); err != nil {
		return err
	}

	runPin := gpioreg.ByName(args.RunPin) // replace with your pin number
	if runPin == nil {
		return fmt.Errorf("failed to find GPIO pin '%s'", args.RunPin)
	}

	bootModePin := gpioreg.ByName(args.BootModePin) // replace with your pin number
	if bootModePin == nil {
		return fmt.Errorf("failed to find GPIO pin '%s'", args.BootModePin)
	}

	log.Println("Driving boot pin low so on next restart the RP2040 will boot in USB mode. Can also be programmed from SWD in this mode.")
	if err := bootModePin.Out(gpio.Low); err != nil {
		return err
	}
	time.Sleep(1 * time.Second)

	log.Println("Restarting RP2040...")
	if err := runPin.Out(gpio.Low); err != nil {
		return err
	}
	time.Sleep(time.Second)
	if err := runPin.Out(gpio.High); err != nil {
		return err
	}

	time.Sleep(10 * time.Second)
	if err := bootModePin.Out(gpio.High); err != nil {
		return err
	}

	log.Println("RP2040 is ready for programming.")

	success := true
	elfToFlash := args.ELF
	if args.EraseFlash {
		elfToFlash = eraseFlashFirmware
	}
	if elfToFlash == "" {
		log.Println("No elf program provided so assuming programming is done manually.")
		log.Println("Press enter when programming is done.")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	} else {
		log.Printf("Programming '%s' using 'openocd' to RP2040\n", elfToFlash)
		cmd := exec.Command("openocd", "-f", "/etc/cacophony/raspberrypi-swd.cfg", "-f", "/target/rp2040.cfg", "-c",
			fmt.Sprintf("program %s verify reset exit", elfToFlash))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			success = false
			log.Printf("Error programming RP2040: %s\n", err)
		}
	}

	if args.EraseFlash {
		// make file to trigger a reprogram next time tc2-agent starts
		if err := os.WriteFile("/etc/cacophony/program_rp2040", []byte{}, 0644); err != nil {
			return err
		}
	}

	log.Println("Releasing Run and Boot mode pins.")
	if err := runPin.In(gpio.Float, gpio.NoEdge); err != nil {
		return err
	}
	if err := bootModePin.In(gpio.Float, gpio.NoEdge); err != nil {
		return err
	}

	details := map[string]any{"success": success}
	if args.EraseFlash {
		details["eraseFlash"] = true
	}
	eventclient.AddEvent(eventclient.Event{
		Timestamp: time.Now(),
		Type:      "programmingRP2040",
		Details:   details,
	})
	if !success {
		return errors.New("failed to program RP2040")
	}

	log.Println("Done.")
	return nil
}

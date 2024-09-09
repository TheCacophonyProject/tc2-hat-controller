package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	logging.LogArgs
}

func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{
		RunPin:      "GPIO23",
		BootModePin: "GPIO5",
	}
	arg.MustParse(&args)
	return args
}

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err)
	}
}

const openOCDNotFoundMessage = `'openocd' was not found. Can be installed using apt 'sudo apt install openocd' or 
following section 5.1 at https://datasheets.raspberrypi.com/pico/getting-started-with-pico.pdf.
If installed using apt then use the config file '/etc/cacophony/raspberrypi-swd.cfg' as 
'interface/raspberrypi-swd.cfg' is not available with the current version provided by apt.`

func runMain() error {
	args := procArgs()

	log = logging.NewLogger(args.LogLevel)

	log.Printf("Running version: %s", version)

	// Check if openocd is installed
	if args.ELF != "" {
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

	log.Println("RP2400 read for programming.")

	success := true
	if args.ELF == "" {
		log.Println("No elf program provided so assuming programming is done manually.")
		log.Println("Press enter when programming is done.")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	} else {
		log.Printf("Programming '%s' using 'openocd' file to RP2040\n", args.ELF)
		cmd := exec.Command("openocd", "-f", "/etc/cacophony/raspberrypi-swd.cfg", "-f", "/target/rp2040.cfg", "-c",
			fmt.Sprintf("program %s verify reset exit", args.ELF))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			success = false
			log.Printf("Error programming RP2040: %s\n", err)
		}
	}

	log.Println("Releasing Run and Boot mode pins.")
	if err := runPin.In(gpio.Float, gpio.NoEdge); err != nil {
		return err
	}
	if err := bootModePin.In(gpio.Float, gpio.NoEdge); err != nil {
		return err
	}

	eventclient.AddEvent(eventclient.Event{
		Timestamp: time.Now(),
		Type:      "programmingRP2040",
		Details:   map[string]interface{}{"success": success},
	})

	log.Println("Done.")
	return nil
}

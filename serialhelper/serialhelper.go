package serialhelper

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/TheCacophonyProject/go-utils/logging"
	"github.com/tarm/serial"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

var log = logging.NewLogger("info")

const cmdlineFile = "/boot/firmware/cmdline.txt"

type SerialUnavailableError struct {
	msg string
}

func (e *SerialUnavailableError) Error() string {
	return e.msg
}

func NewSerialUnavailableError(msg string) error {
	return &SerialUnavailableError{msg: msg}
}

func SerialInUseFromTerminal() bool {
	b, err := os.ReadFile(cmdlineFile)
	if err != nil {
		log.Printf("Error when reading %s: %s", cmdlineFile, err)
		return false
	}
	return strings.Contains(string(b), "console=serial0")
}

// GetSerial will try to get a file lock on the serial port.
// If the file lock can be acquired, it will return the serial file and change mul0 and mul1 to the new values.
// defer ReleaseSerial(serialFile) should be called to release the lock and close the serial file.
func GetSerial(retries int, mul0, mul1 gpio.Level, wait time.Duration) (*os.File, error) {
	// Check if serial is in use by the terminal console.
	if SerialInUseFromTerminal() {
		return nil, NewSerialUnavailableError("serial is in use by the terminal console")
	}

	// Open serial file.
	serialFile, err := os.OpenFile("/dev/serial0", os.O_RDWR, 0666)
	if err != nil {
		return nil, err
	}
	lockAcquired := false
	defer func() {
		if !lockAcquired {
			serialFile.Close()
		}
	}()

	// Try to get a lock on the serial file.
	i := retries
	for {
		err = syscall.Flock(int(serialFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			lockAcquired = true
			break
		}

		if errno, ok := err.(syscall.Errno); ok && errno == syscall.EWOULDBLOCK {
			log.Printf("Serial port is locked. Checking locking process...")
			process, err := getLockingProcess("/dev/serial0")
			if err != nil {
				log.Printf("Error checking locking process: %v", err)
			} else if process == "" {
				log.Printf("No active process found holding the lock. Forcing lock acquisition...")
				// Force unlock by attempting to close and reopen the file
				err := syscall.Flock(int(serialFile.Fd()), syscall.LOCK_UN)
				if err != nil {
					return nil, fmt.Errorf("failed to force unlock: %v", err)
				}
				continue // Retry lock acquisition
			} else {
				log.Printf("Serial port is locked by process: %s", process)
			}

			if i > 0 {
				log.Printf("Serial port is locked by another process. Retrying %d more times in %d seconds...", i, wait/time.Second)
				time.Sleep(wait)
				i--
			} else {
				return nil, NewSerialUnavailableError("failed to get lock on serial, might be in use by other process")
			}
		} else {
			return nil, err
		}
	}

	// Configure GPIO pins for the UART multiplexer as requested.
	if _, err := host.Init(); err != nil {
		log.Fatal(err)
	}
	mul0Pin := gpioreg.ByName("GPIO6")
	if mul0Pin == nil {
		return nil, fmt.Errorf("failed to init GPIO6 pin")
	}
	if err := mul0Pin.Out(mul0); err != nil {
		return nil, err
	}
	mul1Pin := gpioreg.ByName("GPIO12")
	if mul1Pin == nil {
		return nil, fmt.Errorf("failed to init GPIO12 pin")
	}
	if err := mul1Pin.Out(mul1); err != nil {
		return nil, err
	}

	out, err := exec.Command("raspi-gpio", "set", "14", "a0").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to set GPIO14 to a0(UART): %v, output: %s", err, out)
	}
	out, err = exec.Command("raspi-gpio", "set", "15", "a0").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to set GPIO15 to a0(UART): %v, output: %s", err, out)
	}

	return serialFile, nil
}

func getLockingProcess(serialPath string) (string, error) {
	// Run `fuser` to check which process is using the file
	cmd := exec.Command("fuser", serialPath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
			// Exit code 1 from `fuser` means no process is using the file
			return "", nil
		}
		return "", fmt.Errorf("failed to execute fuser: %v", err)
	}
	return output.String(), nil
}

func ReleaseSerial(serialFile *os.File) error {
	serialFile.Close()
	return syscall.Flock(int(serialFile.Fd()), syscall.LOCK_UN)
}

func SerialSendReceive(retries int, mul0, mul1 gpio.Level, wait time.Duration, data []byte, baud int) ([]byte, error) {
	serialFile, err := GetSerial(retries, mul0, mul1, wait)
	if err != nil {
		return nil, err
	}

	defer ReleaseSerial(serialFile)

	c := &serial.Config{Name: "/dev/serial0", Baud: baud, ReadTimeout: time.Second * 5}
	serialPort, err := serial.OpenPort(c)
	if err != nil {
		return nil, err
	}
	defer serialPort.Close()

	n, err := serialPort.Write(data)
	if err != nil {
		return nil, err
	}
	if n != len(data) {
		return nil, fmt.Errorf("wrote %d bytes, expected %d", n, len(data))
	}
	time.Sleep(time.Second)
	buf := make([]byte, 256)
	n, err = serialPort.Read(buf)
	log.Infof("Received %d bytes", n)
	log.Info("Received:", buf[:n])
	if err != nil {
		return nil, err
	}

	return buf[:n], nil
}

func SerialSend(retries int, mul0, mul1 gpio.Level, wait time.Duration, data []byte, baud int) error {
	start := time.Now()

	serialFile, err := GetSerial(retries, mul0, mul1, wait)
	if err != nil {
		return err
	}
	defer ReleaseSerial(serialFile)

	elapsed := time.Since(start)
	log.Print("Serial lock took ", elapsed)

	start = time.Now()
	c := &serial.Config{Name: "/dev/serial0", Baud: baud, ReadTimeout: time.Second * 5}
	serialPort, err := serial.OpenPort(c)
	if err != nil {
		return err
	}
	defer serialPort.Close()
	elapsed = time.Since(start)
	log.Println("Serial open took ", elapsed)

	start = time.Now()
	n, err := serialPort.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("wrote %d bytes, expected %d", n, len(data))
	}
	elapsed = time.Since(start)
	log.Print("Serial send took ", elapsed)

	return nil
}

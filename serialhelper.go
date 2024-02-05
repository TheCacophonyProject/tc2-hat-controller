package serialhelper

import (
	"fmt"
	"log"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/tarm/serial"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

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
			if i > 0 {
				fmt.Printf("Serial port is locked by another process. Retrying %d in 5 seconds...", i)
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

	return serialFile, nil
}

func ReleaseSerial(serialFile *os.File) error {
	serialFile.Close()
	return syscall.Flock(int(serialFile.Fd()), syscall.LOCK_UN)
}

func SerialSendReceive(retries int, mul0, mul1 gpio.Level, wait time.Duration, data []byte) ([]byte, error) {

	serialFile, err := GetSerial(retries, mul0, mul1, wait)
	if err != nil {
		return nil, err
	}

	defer ReleaseSerial(serialFile)

	c := &serial.Config{Name: "/dev/serial0", Baud: 9600, ReadTimeout: time.Second * 5}
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
	if err != nil {
		return nil, err
	}

	return buf[:n], nil
}

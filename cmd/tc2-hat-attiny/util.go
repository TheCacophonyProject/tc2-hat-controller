package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
)

func shutdown(a *attiny) error {
	err := a.writeCameraState(statePoweringOff) // Without setting the state to powering off the ATtiny will automatically reboot the RPi.
	if err != nil {
		return err
	}
	time.Sleep(5 * time.Second)
	log.Println("Powering off")
	output, err := exec.Command("/sbin/poweroff").CombinedOutput()
	if err != nil {
		return fmt.Errorf("power off failed: %v\n%s", err, output)
	}
	return nil
}

// shouldStayOnForSalt will check if a salt command is running via checking the output from `salt-call saltutil.running`
// If a device is being kept on for too long because of salt commands it will ignore the salt command check.
func shouldStayOnForSalt() bool {
	if saltCommandWaitEnd.IsZero() {
		saltCommandWaitEnd = time.Now().Add(saltCommandMaxWaitDuration)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stdout, err := exec.CommandContext(ctx, "salt-call", "--local", "saltutil.running").Output()
	if err != nil {
		log.Println(err)
		return false
	}

	strOut := string(stdout)
	if strings.Count(strOut, "\n") <= 2 { // If a salt command is running the output will have much more than 2 lines.
		return false
	}

	if time.Now().After(saltCommandWaitEnd) {
		log.Printf("waiting for salt command for too long (%v)", saltCommandMaxWaitDuration)
		log.Printf("salt command:\n%v", strOut)
		return false
	}
	log.Println("staying on for salt command to finish")
	return true
}

func durToStr(duration time.Duration) string {
	return duration.Truncate(time.Second).String()
}

func crcTxWithRetry(write, read []byte) error {
	attempts := 0
	for {
		err := crcTX(write, read)
		if err == nil {
			return nil
		}

		attempts++
		if attempts >= maxTxAttempts {
			return err
		}
		time.Sleep(txRetryInterval)
	}
}

func crcTX(write, read []byte) error {
	response, err := i2crequest.TxWithCRC(0x25, write, len(read), 1000)
	if err != nil {
		return err
	}
	for i := 0; i < len(read); i++ {
		read[i] = response[i]
	}
	return nil
}

func checkServiceStatus(serviceName string) (bool, error) {
	cmd := exec.Command("systemctl", "is-active", "--quiet", serviceName)
	err := cmd.Run()
	if err == nil {
		// Service is active
		return true, nil
	} else if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 3 {
		// Service is inactive
		return false, nil
	}
	// An error occurred
	return false, err
}

func calculateSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func absDiff(a, b uint16) uint16 {
	if a > b {
		return a - b
	}
	return b - a
}

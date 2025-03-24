package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/TheCacophonyProject/go-utils/saltutil"
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
	if !saltutil.IsSaltIdSet() {
		return false
	}

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

func calculateMean(values []uint16) float64 {
	sum := 0.0
	for _, value := range values {
		sum += float64(value)
	}
	return sum / float64(len(values))
}

func calculateStandardDeviation(values []uint16) float64 {
	mean := calculateMean(values)
	var varianceSum float64
	for _, value := range values {
		diff := float64(value) - mean
		varianceSum += diff * diff
	}
	variance := varianceSum / float64(len(values))
	return math.Sqrt(variance)
}

func getResistorDividerValuesFromVersion(hardwareVer versionStr, resistorValues []rVals) (float32, float32, float32, error) {
	// Find the resistor and voltage values for the given hardware version
	var vref, r1, r2 float32 = 0, 0, 0
	for _, v := range resistorValues {
		newer, err := hardwareVer.IsNewerOrEqual(v.hardwareVersion)
		if err != nil {
			return 0, 0, 0, err
		}
		if newer {
			vref = v.vref
			r1 = v.r1
			r2 = v.r2
		}
	}

	return vref, r1, r2, nil
}

type rVals struct {
	hardwareVersion versionStr
	vref, r1, r2    float32
}

type versionStr string

func (v versionStr) IsNewerOrEqual(other versionStr) (bool, error) {

	versionStr := strings.TrimPrefix(string(v), "v")
	parts := strings.Split(versionStr, ".")
	partsInts := make([]int, len(parts))
	var err error
	for i, part := range parts {
		partsInts[i], err = strconv.Atoi(part)
		if err != nil {
			return false, err
		}
	}
	if len(parts) != 3 {
		return false, fmt.Errorf("invalid version format '%s", v)
	}

	otherVersionStr := strings.TrimPrefix(string(other), "v")
	partsOther := strings.Split(otherVersionStr, ".")
	partsOtherInts := make([]int, len(partsOther))
	for i, part := range partsOther {
		partsOtherInts[i], err = strconv.Atoi(part)
		if err != nil {
			return false, err
		}
	}
	if len(partsOtherInts) != 3 {
		return false, fmt.Errorf("invalid version format '%s", other)
	}
	if partsInts[0] != partsOtherInts[0] {
		return partsInts[0] > partsOtherInts[0], nil
	}
	if partsInts[1] != partsOtherInts[1] {
		return partsInts[1] > partsOtherInts[1], nil
	}
	return partsInts[2] >= partsOtherInts[2], nil
}

// Check if the service is running
func isServiceRunning(serviceName string) (bool, error) {
	cmd := exec.Command("systemctl", "is-active", "--quiet", serviceName)
	err := cmd.Run()
	if err == nil {
		return true, nil // Service is running
	}
	if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 3 {
		return false, nil // Service is not running
	}
	return false, fmt.Errorf("failed to check service status: %v", err)
}

// Start or stop a service
func manageService(action, serviceName string) error {
	cmd := exec.Command("systemctl", action, serviceName)
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to %s service %s: %v", action, serviceName, err)
	}
	log.Printf("Service %s %sd successfully.\n", serviceName, action)
	return nil
}

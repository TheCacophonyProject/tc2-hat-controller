package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"periph.io/x/conn/v3/i2c"
)

func checkIfRunningHotspot() (bool, error) {
	cmd := exec.Command("iw", "dev", "wlan0", "info")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()

	if err != nil {
		return false, err
	}

	ssidRegex := regexp.MustCompile(`ssid ([^\s]+)`)
	typeRegex := regexp.MustCompile(`type ([^\s]+)`)

	ssidMatch := ssidRegex.FindStringSubmatch(out.String())
	typeMatch := typeRegex.FindStringSubmatch(out.String())

	var ssid, wifiType string

	if len(ssidMatch) > 1 {
		ssid = ssidMatch[1]
	}

	if len(typeMatch) > 1 {
		wifiType = typeMatch[1]
	}

	return ssid == "bushnet" && wifiType == "AP", nil
}

/*
func checkIsConnectedToNetwork() (bool, error) {
	cmd := exec.Command("wpa_cli", "-i", "wlan0", "status")
	output, err := cmd.CombinedOutput()
	if err != nil {
		if err != nil {
			return false, fmt.Errorf("error executing wpa_cli: %w", err)
		}
	}

	ssid := ""
	ipAddress := ""
	stateCompleted := false
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ssid=") {
			ssid = strings.TrimPrefix(line, "ssid=")
		}
		if strings.HasPrefix(line, "ip_address=") {
			ipAddress = strings.TrimPrefix(line, "ip_address=")
		}
		if strings.Contains(line, "wpa_state=COMPLETED") {
			stateCompleted = true
		}
	}
	// When connecting to a network with the wrong password and wpa_state can be 'COMPLETED',
	// so to check that it has the correct password we also check for an ip address.
	if stateCompleted && ssid != "" && ipAddress != "" {
		log.Printf("Connected to '%s' with address '%s'", ssid, ipAddress)
		return true, nil
	} else {
		return false, nil
	}
}
*/

/*
// checkWifiConnection checks if the wifi connection is active by checking that the state is COMPLETED and that it had a ip address.
// If connecting to a network that has the wrong password the state can be COMPLETED, so also checking for the ip address is needed.

	func getNetworkState() (string, error) {
		state, err := managementdclient.GetNetworkState()
		if err != nil {
			return "", err
		}
		log.Println(state)
		if state != "WIFI" {
			return state, nil
		}
		connected, err := checkIsConnectedToNetwork()
		if err != nil {
			log.Println("Error checking if connected to network:", err)
			return state, nil
		}
		if connected {
			return "WIFI_CONNECTED", nil
		} else {
			return state, nil
		}
		/*
			cmd := exec.Command("wpa_cli", "-i", "wlan0", "status")
			output, err := cmd.CombinedOutput()
			if err != nil {
				return false, fmt.Errorf("error executing wpa_cli: %w", err)
			}

			ssid := ""
			ipAddress := ""
			stateCompleted := false
			// Parse the output
			scanner := bufio.NewScanner(strings.NewReader(string(output)))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "ssid=") {
					ssid = strings.TrimPrefix(line, "ssid=")
				}
				if strings.HasPrefix(line, "ip_address=") {
					ipAddress = strings.TrimPrefix(line, "ip_address=")
				}
				if strings.Contains(line, "wpa_state=COMPLETED") {
					stateCompleted = true
				}
			}
			if stateCompleted && ssid != "" && ipAddress != "" {
				log.Printf("Connected to '%s' with address '%s'", ssid, ipAddress)
				return true, nil
			} else {
				return false, nil
			}

}
*/
func fromBCD(b byte) int {
	return int(b&0x0F) + int(b>>4)*10
}

// readBytes reads bytes from the I2C device starting from a given register.
func readBytes(dev *i2c.Dev, register byte, data []byte) error {
	return dev.Tx([]byte{register}, data)
}

// readByte reads a byte from the I2C device from a given register.
func readByte(dev *i2c.Dev, register byte) (byte, error) {
	data := make([]byte, 1)
	if err := dev.Tx([]byte{register}, data); err != nil {
		return 0, err
	}
	return data[0], nil
}

// writeByte writes a byte to the I2C device at a given register.
func writeByte(dev *i2c.Dev, register byte, data byte) error {
	_, err := dev.Write([]byte{register, data})
	return err
}

// toBCD converts a decimal number to binary-coded decimal.
func toBCD(n int) byte {
	return byte(n)/10<<4 + byte(n)%10
}

// writeBytes writes the given bytes to the I2C device.
func writeBytes(dev *i2c.Dev, data []byte) error {
	_, err := dev.Write(data)
	return err
}

func shutdown() error {
	cmd := exec.Command("/sbin/poweroff")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("power off failed: %v\n%s", err, output)
	}
	return nil
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
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

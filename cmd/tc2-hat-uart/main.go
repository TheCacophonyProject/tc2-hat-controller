package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	serialhelper "github.com/TheCacophonyProject/tc2-hat-controller"
	"github.com/alexflint/go-arg"
	"periph.io/x/conn/v3/gpio"
)

var (
	version           = "<not set>"
	activateTrapUntil = time.Now()
	activeTrapSig     = make(chan string)
)

type Args struct {
}

func (Args) Version() string {
	return version
}

func procArgs() Args {
	args := Args{}
	arg.MustParse(&args)
	return args
}

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err)
	}
}

// Message represents the data structure for communication with a device connected on UART.
// - ID: Identifier of the message being sent or the message being responded to.
// - Response: Indicates if the message is a response.
// - Type: Specifies the type of message (e.g., write, read, command, ACK, NACK).
// - Data: Contains the actual data payload, which varies depending on the type or response.
type Message struct {
	ID       int    `json:"id,omitempty"`
	Response bool   `json:"response,omitempty"`
	Type     string `json:"type,omitempty"`
	Data     string `json:"data,omitempty"`
}

type Command struct {
	Command string `json:"command"`
	Args    string `json:"args,omitempty"`
}

type Write struct {
	Var string      `json:"var,omitempty"`
	Val interface{} `json:"val,omitempty"`
}

type Read struct {
	Var string `json:"var,omitempty"`
}

type ReadResponse struct {
	Val string `json:"var,omitempty"`
}

func checkClassification(data map[byte]byte) error {
	for k, v := range data {
		if k == 1 && v > 80 {
			activateTrap()
		}
		if k == 7 && v > 80 {
			activateTrap()
		}
	}
	return nil
}

func activateTrap() {
	log.Println("Activating trap")
	activateTrapUntil = time.Now().Add(time.Minute)
	activeTrapSig <- "trap"
}

func runMain() error {
	log.SetFlags(0) // Removes default timestamp flag
	log.Printf("running version: %s", version)

	args := procArgs()
	log.Println(args)

	// Start dbus to listen for classification messages.

	if err := beep(); err != nil {
		return err
	}

	log.Println("Starting UART service")
	if err := startService(); err != nil {
		return err
	}

	trapActive := false
	if err := sendTrapActiveState(trapActive); err != nil {
		return err
	}

	for {
		waitUntil := time.Now().Add(5 * time.Second)
		if trapActive {
			waitUntil = activateTrapUntil
		}

		select {
		case <-activeTrapSig:
		case <-time.After(time.Until(waitUntil)):
		}
		trapActive = time.Now().Before(activateTrapUntil)

		if err := sendTrapActiveState(trapActive); err != nil {
			return err
		}
	}
}

func sendTrapActiveState(active bool) error {
	return sendWriteMessage("active", active)
}

func sendWriteMessage(varName string, val interface{}) error {
	data, err := json.Marshal(&Write{
		Var: varName,
		Val: val,
	})
	if err != nil {
		return err
	}
	message := Message{
		Type: "write",
		Data: string(data),
	}
	response, err := sendMessage(message)
	if err != nil {
		return err
	}
	if response.Type == "NACK" {
		return fmt.Errorf("NACK response")
	}
	return nil
}

func beep() error {
	log.Println("beep")
	return sendCommandMessage("beep")
}

func sendCommandMessage(cmd string) error {
	data, err := json.Marshal(&Command{
		Command: cmd,
	})
	if err != nil {
		return err
	}
	message := Message{
		Type: "command",
		Data: string(data),
	}
	response, err := sendMessage(message)
	if err != nil {
		return err
	}
	if response.Type == "NACK" {
		return fmt.Errorf("NACK response")
	}
	return nil
}

func sendReadMessage(varName string) (string, error) {
	data, err := json.Marshal(&Read{
		Var: varName,
	})
	if err != nil {
		return "", err
	}
	message := Message{
		Type: "read",
		Data: string(data),
	}
	response, err := sendMessage(message)
	if err != nil {
		return "", err
	}
	if response.Type == "NACK" {
		return "", fmt.Errorf("NACK response")
	}
	readResponse := &ReadResponse{}
	if err := json.Unmarshal([]byte(response.Data), readResponse); err != nil {
		return "", err
	}
	return readResponse.Val, nil
}

func checkPIR(oldPirVal int) (int, error) {
	valStr, err := sendReadMessage("pir")
	if err != nil {
		return 0, err
	}
	newPirVal, err := strconv.Atoi(valStr)
	if err != nil {
		return 0, err
	}
	if oldPirVal != newPirVal {
		//TODO Make event
		log.Println("New pir value:", newPirVal)
	}
	return newPirVal, nil
}

func computeChecksum(message []byte) int {
	checksum := 0
	for _, b := range message {
		checksum += int(b)
	}
	return checksum % 256
}

func sendMessage(cmd Message) (*Message, error) {
	cmdData, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	message := fmt.Sprintf("<%s|%d>", cmdData, computeChecksum(cmdData))

	log.Println("Message: ", message)
	responseData, err := serialhelper.SerialSendReceive(3, gpio.High, gpio.Low, time.Second, []byte(message))

	if err != nil {
		return nil, err
	}
	log.Println("Response: ", string(responseData))

	if responseData[0] != '<' {
		return nil, fmt.Errorf("response doesn't start with '<'")
	}
	if responseData[len(responseData)-1] != '>' {
		return nil, fmt.Errorf("response doesn't end with '>'")
	}

	// Extract and verify message and checksum
	responseData = responseData[1 : len(responseData)-1]
	parts := bytes.Split(responseData, []byte("|"))
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid response format")
	}
	log.Println("Response:", string(parts[0]))
	receivedChecksum, err := strconv.Atoi(string(parts[1]))
	if err != nil {
		return nil, err
	}
	if computeChecksum(parts[0]) != receivedChecksum {
		return nil, fmt.Errorf("checksum mismatch")
	}

	// Unmarshal response to a Message
	responseMessage := &Message{}
	log.Println(string(parts[0]))
	return responseMessage, json.Unmarshal(parts[0], responseMessage)
}

package main

import (
	"log"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/serialhelper"
	"periph.io/x/conn/v3/gpio"
)

func main() {
	err := runMain()
	if err != nil {
		log.Fatal(err)
	}
}

func runMain() error {
	log.Println("Setting up serial helper for the ATtiny")
	serialFile, err := serialhelper.GetSerial(3, gpio.Low, gpio.Low, time.Second)
	if err != nil {
		return err
	}
	log.Println("Serial acquired")

	time.Sleep(20 * time.Second)
	log.Println("Releasing serial")
	serialhelper.ReleaseSerial(serialFile)
	return nil
}

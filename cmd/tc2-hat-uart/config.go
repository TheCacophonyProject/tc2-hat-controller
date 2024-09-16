package main

import (
	"github.com/TheCacophonyProject/go-config"
)

type CommsConfig struct {
	config.Comms

	UartTxPin string
}

func ParseCommsConfig(configDir string) (*CommsConfig, error) {
	conf, err := config.New(configDir)
	if err != nil {
		return nil, err
	}

	c := config.DefaultComms()
	if err := conf.Unmarshal(config.CommsKey, &c); err != nil {
		return nil, err
	}

	gpio := config.DefaultGPIO()
	if err := conf.Unmarshal(config.GPIOKey, &gpio); err != nil {
		return nil, err
	}

	return &CommsConfig{
		Comms:     c,
		UartTxPin: gpio.UartTx,
	}, nil
}

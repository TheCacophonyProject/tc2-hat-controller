package comms

import (
	"github.com/TheCacophonyProject/go-config"
	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
)

type CommsConfig struct {
	config.Comms

	TrapSpecies    tracks.Species
	ProtectSpecies tracks.Species

	UartTxPin string
	BaudRate  int
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
		Comms:          	c,
		TrapSpecies:    	tracks.Species(c.TrapSpecies),
		ProtectSpecies: 	tracks.Species(c.ProtectSpecies),
		UartTxPin:      	gpio.UartTx,
	}, nil
}

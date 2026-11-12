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

	TrapConfig config.Trap

	// LowPowerMode is set when the RP2040 does the recording rather than the Raspberry Pi.
	// The Pi is then only on now and then, so it doesn't enable the trap.
	LowPowerMode bool
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

	// A zero or negative duration would mean the trap is never enabled (or never protected),
	// so fall back to the default instead.
	defaults := config.DefaultComms()
	if c.TrapDuration <= 0 {
		log.Warnf("Invalid trap-duration '%s', using default of '%s'", c.TrapDuration, defaults.TrapDuration)
		c.TrapDuration = defaults.TrapDuration
	}
	if c.ProtectDuration <= 0 {
		log.Warnf("Invalid protect-duration '%s', using default of '%s'", c.ProtectDuration, defaults.ProtectDuration)
		c.ProtectDuration = defaults.ProtectDuration
	}

	gpio := config.DefaultGPIO()
	if err := conf.Unmarshal(config.GPIOKey, &gpio); err != nil {
		return nil, err
	}

	trap := config.DefaultTrap()
	if err := conf.Unmarshal(config.TrapKey, &trap); err != nil {
		return nil, err
	}

	recorder := config.DefaultThermalRecorder()
	if err := conf.Unmarshal(config.ThermalRecorderKey, &recorder); err != nil {
		return nil, err
	}

	return &CommsConfig{
		Comms:          c,
		TrapSpecies:    tracks.Species(c.TrapSpecies),
		ProtectSpecies: tracks.Species(c.ProtectSpecies),
		UartTxPin:      gpio.UartTx,
		TrapConfig:     trap,
		LowPowerMode:   recorder.UseLowPowerMode,
	}, nil
}

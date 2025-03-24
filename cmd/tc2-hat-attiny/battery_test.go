package main

import (
	"testing"

	"github.com/TheCacophonyProject/go-config"
	"github.com/stretchr/testify/assert"
)

func TestBatterySelectionFromVoltage(t *testing.T) {
	c := config.DefaultBattery()

	batType, err := getBatteryType(&c, 2.8)
	assert.NoError(t, err)
	assert.Nil(t, batType)
	batType, err = getBatteryType(&c, 2.9)
	assert.NoError(t, err)
	assert.Equal(t, batType.Name, config.LiIonBattery.Name)
	batType, err = getBatteryType(&c, 4.3)
	assert.NoError(t, err)
	assert.Equal(t, batType.Name, config.LiIonBattery.Name)
	batType, err = getBatteryType(&c, 4.4)
	assert.NoError(t, err)
	assert.Nil(t, batType)

	batType, err = getBatteryType(&c, 8.9)
	assert.NoError(t, err)
	assert.Nil(t, batType)
	batType, err = getBatteryType(&c, 9)
	assert.NoError(t, err)
	assert.Equal(t, batType.Name, config.LeadAcid12V.Name)
	batType, err = getBatteryType(&c, 14)
	assert.NoError(t, err)
	assert.Equal(t, batType.Name, config.LeadAcid12V.Name)
	batType, err = getBatteryType(&c, 14.1)
	assert.NoError(t, err)
	assert.Nil(t, batType)

	batType, err = getBatteryType(&c, 28.9)
	assert.NoError(t, err)
	assert.Nil(t, batType)
	batType, err = getBatteryType(&c, 29)
	assert.NoError(t, err)
	assert.Equal(t, batType.Name, config.LimeBattery.Name)
	batType, err = getBatteryType(&c, 42.5)
	assert.NoError(t, err)
	assert.Equal(t, batType.Name, config.LimeBattery.Name)
	batType, err = getBatteryType(&c, 42.6)
	assert.NoError(t, err)
	assert.Nil(t, batType)
}

func TestBatterySetFromConfig(t *testing.T) {
	// Testing using custom battery
	c := config.Battery{
		CustomBatteryType: &config.BatteryType{
			Name:       "custom",
			MinVoltage: 1,
			MaxVoltage: 2,
			Voltages:   []float32{10, 20},
			Percent:    []float32{0, 100},
		},
	}
	batType, err := getBatteryType(&c, 1.1)
	assert.NoError(t, err)
	assert.Equal(t, batType.Name, "custom")

	// Testing using preset battery type
	c = config.Battery{
		PresetBatteryType: "li-ion",
	}
	batType, err = getBatteryType(&c, 44)
	assert.NoError(t, err)
	assert.Equal(t, batType.Name, config.LiIonBattery.Name)

	// Testing using preset battery not found
	c = config.Battery{
		PresetBatteryType: "foobar",
	}
	batType, err = getBatteryType(&c, 44)
	assert.Error(t, err)
	assert.Nil(t, batType)
}

package attiny

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComparingVersions(t *testing.T) {
	newVersion := versionStr("1.2.3")
	oldVersion := versionStr("1.2.2")

	newer, err := newVersion.IsNewerOrEqual(oldVersion)
	assert.NoError(t, err)
	assert.True(t, newer)

	notNewer, err := oldVersion.IsNewerOrEqual(newVersion)
	assert.NoError(t, err)
	assert.False(t, notNewer)

	newVersion = versionStr("1.2.3")
	oldVersion = versionStr("1.1.5")

	newer, err = newVersion.IsNewerOrEqual(oldVersion)
	assert.NoError(t, err)
	assert.True(t, newer)

	notNewer, err = oldVersion.IsNewerOrEqual(newVersion)
	assert.NoError(t, err)
	assert.False(t, notNewer)

	newVersion = versionStr("1.2.3")
	oldVersion = versionStr("0.4.4")

	newer, err = newVersion.IsNewerOrEqual(oldVersion)
	assert.NoError(t, err)
	assert.True(t, newer)

	notNewer, err = oldVersion.IsNewerOrEqual(newVersion)
	assert.NoError(t, err)
	assert.False(t, notNewer)

	equal, err := oldVersion.IsNewerOrEqual(oldVersion)
	assert.NoError(t, err)
	assert.True(t, equal)
}

func TestGetResistorDividerValuesFromVersion(t *testing.T) {
	newVersion := versionStr("v0.1.4")

	vref, r1, r2, err := getResistorDividerValuesFromVersion(newVersion, lvResistorVals)
	assert.NoError(t, err)
	assert.Equal(t, float32(3.3), vref)
	assert.Equal(t, float32(2000), r1)
	assert.Equal(t, float32(560+33), r2)
}

func TestCalculatingBatteryVoltages(t *testing.T) {
	tolerance := 0.001
	raw := uint16(1023)

	// Low battery voltage check for version 0.1.4
	batteryVal, err := calculateBatteryVoltage(raw, versionStr("v0.1.4"), lvResistorVals)
	assert.NoError(t, err)
	assert.InDelta(t, float32(14.4298482293), batteryVal, tolerance)

	// High battery voltage check for version 0.1.4
	batteryVal, err = calculateBatteryVoltage(raw, versionStr("0.1.4"), hvResistorVals)
	assert.NoError(t, err)
	assert.InDelta(t, float32(41.6720930233), batteryVal, tolerance)

	// Low battery voltage check for version 0.4.0
	batteryVal, err = calculateBatteryVoltage(raw, versionStr("v0.4.0"), lvResistorVals)
	assert.NoError(t, err)
	assert.InDelta(t, float32(13.1044117647), batteryVal, tolerance)

	// High battery voltage check for version 0.1.4
	batteryVal, err = calculateBatteryVoltage(raw, versionStr("v0.4.0"), hvResistorVals)
	assert.NoError(t, err)
	assert.InDelta(t, float32(42.9083333333), batteryVal, tolerance)

	// Low battery voltage check for version 0.7.0
	batteryVal, err = calculateBatteryVoltage(raw, versionStr("0.7.0"), lvResistorVals)
	assert.NoError(t, err)
	assert.InDelta(t, float32(17.3425531915), batteryVal, tolerance)

	// High battery voltage check for version 0.7.0
	batteryVal, err = calculateBatteryVoltage(raw, versionStr("v0.7.0"), hvResistorVals)
	assert.NoError(t, err)
	assert.InDelta(t, float32(42.5857142857), batteryVal, tolerance)

}

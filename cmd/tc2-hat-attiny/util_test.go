package main

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

func TestGettingResistorValues(t *testing.T) {
	vref, r1, r2, err := getResistorDividerValuesFromVersion(versionStr("0.1.4"), hvResistorVals)

	assert.NoError(t, err)
	assert.Equal(t, float32(3.3), vref)
	assert.Equal(t, float32(2000), r1)
	assert.Equal(t, float32(560+33), r2)
}

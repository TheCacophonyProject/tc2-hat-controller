package eeprom

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDeepEqualV1(t *testing.T) {
	data1V1 := EepromDataV1{
		Version: 1,
		Time:    time.Now().Truncate(time.Second),
		Major:   2,
		Minor:   3,
		Patch:   4,
		ID:      5,
	}
	data2V1 := EepromDataV1{
		Version: 1,
		Time:    time.Now().Truncate(time.Second),
		Major:   2,
		Minor:   3,
		Patch:   4,
		ID:      5,
	}
	data3V1 := EepromDataV1{
		Version: 1,
		Time:    time.Now().Truncate(time.Second),
		Major:   2,
		Minor:   3,
		Patch:   4,
		ID:      6,
	}
	assert.True(t, reflect.DeepEqual(data1V1, data2V1))
	assert.False(t, reflect.DeepEqual(data1V1, data3V1))
}

func TestDeepEqualV2(t *testing.T) {
	data1V2 := EepromDataV2{
		Version: 2,
		MainPCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		PowerPCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		MicrophonePCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		TouchPCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		Time:      time.Now().Truncate(time.Second),
		ID:        5,
		AudioOnly: true,
	}
	data2V2 := EepromDataV2{
		Version: 2,
		MainPCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		PowerPCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		MicrophonePCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		TouchPCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		Time:      time.Now().Truncate(time.Second),
		ID:        5,
		AudioOnly: true,
	}
	data3V2 := EepromDataV2{
		Version: 2,
		MainPCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		PowerPCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		MicrophonePCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 3},
		TouchPCB: SemVer{
			Major: 1,
			Minor: 2,
			Patch: 4},
		Time:      time.Now().Truncate(time.Second),
		ID:        5,
		AudioOnly: true,
	}
	assert.True(t, reflect.DeepEqual(data1V2, data2V2))
	assert.False(t, reflect.DeepEqual(data1V2, data3V2))
}

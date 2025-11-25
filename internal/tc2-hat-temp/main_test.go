/*
SHT3x - Connecting to the AHT20 sensor.
Copyright (C) 2024, The Cacophony Project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package temp

import (
	"fmt"
	"testing"
	"time"

	"github.com/TheCacophonyProject/tc2-hat-controller/i2crequest"
	"github.com/stretchr/testify/require"
)

var noSleepFn = func(d time.Duration) {}

func addCRC(data []byte) []byte {
	crc := calculateCRC(data)
	return append(data, crc)
}

func TestGoodReading(t *testing.T) {
	sleepFn = noSleepFn
	i2crequest.MockTxResponses([]i2crequest.TxResponse{
		{
			Response: []byte{},
			Err:      nil,
		},
		{
			Response: addCRC([]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}),
			Err:      nil,
		},
	})
	temp, humidity, err := makeReading()
	require.NoError(t, err)
	require.Equal(t, -50.0, temp)
	require.Equal(t, 0.0, humidity)

	// TODO, Add test with non 0 responses.
}

func Repeat[T any](in []T, n int) []T {
	out := make([]T, 0, len(in)*n)
	for range n {
		out = append(out, in...)
	}
	return out
}

func TestBadCRC(t *testing.T) {
	sleepFn = noSleepFn
	responses := []i2crequest.TxResponse{
		{
			Response: []byte{},
			Err:      nil,
		},
		{
			Response: []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01},
			Err:      nil,
		},
	}
	responses = Repeat(responses, 3)
	i2crequest.MockTxResponses(responses)
	_, _, err := makeReading()
	require.Equal(t, errBadCRC, err)
	require.Fail(t, "test")
}

func TestNoCRCGoodData(t *testing.T) {
	sleepFn = noSleepFn
	responses := []i2crequest.TxResponse{
		{
			Response: []byte{},
			Err:      nil,
		},
		{
			Response: []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF},
			Err:      nil,
		},
	}
	responses = Repeat(responses, 4)
	i2crequest.MockTxResponses(responses)
	temp, humidity, err := makeReading()
	require.NoError(t, err)
	require.Equal(t, -50.0, temp)
	require.Equal(t, 0.0, humidity)
}

func TestNoCRCBadData(t *testing.T) {
	sleepFn = noSleepFn
	responses := []i2crequest.TxResponse{
		{
			Response: []byte{},
			Err:      nil,
		},
		{
			Response: []byte{0x00, 0x00, 0x00, 0xFF, 0x00, 0x00, 0xFF},
			Err:      nil,
		},
		{
			Response: []byte{},
			Err:      nil,
		},
		{
			Response: []byte{0x00, 0x00, 0x00, 0xFF, 0x00, 0x00, 0xFF},
			Err:      nil,
		},
		{
			Response: []byte{},
			Err:      nil,
		},
		{
			Response: []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF},
			Err:      nil,
		},
		{
			Response: []byte{},
			Err:      nil,
		},
		{
			Response: []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF},
			Err:      nil,
		},
	}
	log.Println(responses)
	i2crequest.MockTxResponses(responses)
	_, _, err := makeReading()
	require.Error(t, err)
}

func TestErrorMakingReadings(t *testing.T) {
	sleepFn = noSleepFn
	expectedErr := fmt.Errorf("foo")
	responses := []i2crequest.TxResponse{
		{
			Response: []byte{},
			Err:      expectedErr,
		},
		{
			Response: []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF},
			Err:      nil,
		},
	}
	responses = Repeat(responses, 3)
	i2crequest.MockTxResponses(responses)
	_, _, err := makeReading()
	require.Equal(t, err, expectedErr)
}

func TestReadyBitNotGettingSet(t *testing.T) {
	sleepFn = noSleepFn
	responses := []i2crequest.TxResponse{
		{
			Response: []byte{},
			Err:      nil,
		},
		{
			Response: addCRC([]byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x00}),
			Err:      nil,
		},
		{
			Response: addCRC([]byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x00}),
			Err:      nil,
		},
		{
			Response: addCRC([]byte{0xFF, 0x00, 0x00, 0x00, 0x00, 0x00}),
			Err:      nil,
		},
	}
	responses = Repeat(responses, 3)
	i2crequest.MockTxResponses(responses)
	_, _, err := makeReading()
	require.Error(t, err)
}

// TODO: Test a combination of different types of errors all together

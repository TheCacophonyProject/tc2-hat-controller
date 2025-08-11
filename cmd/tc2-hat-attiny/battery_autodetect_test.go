package main

import (
	"path/filepath"
	"testing"

	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpecificVoltageDetection tests critical integration scenarios for voltage detection
// Note: Comprehensive voltage detection unit tests are in go-config/battery_autodetect_test.go
func TestSpecificVoltageDetection(t *testing.T) {
	tests := []struct {
		name              string
		voltage           float32
		expectedChemistry string
		expectedCells     int
		description       string
	}{
		{
			name:              "3.86V should detect Li-ion 1 cell",
			voltage:           3.86,
			expectedChemistry: "li-ion",
			expectedCells:     1,
			description:       "Integration test: overlapping range preference through BatteryMonitor",
		},
		{
			name:              "6.6V should detect LiFePO4 2 cells",
			voltage:           6.6,
			expectedChemistry: "lifepo4",
			expectedCells:     2,
			description:       "Integration test: LiFePO4 detection through BatteryMonitor",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create temporary state directory for test
			stateDir := t.TempDir()

			// Initialize battery monitor with default config
			batteryConfig := goconfig.DefaultBattery()

			monitor := &BatteryMonitor{
				config:              &batteryConfig,
				voltageHistory:      make([]timestampedVoltage, 0, voltageHistorySize),
				lastReportedPercent: -1,
				stateFilePath:       filepath.Join(stateDir, "battery_state.json"),
				dischargeRateAlpha:  0.1,
				dischargeRateWindow: make([]float32, 0, 20),
				lastDisplayedHours:  -1,
				observedMinVoltage:  999.0,
				observedMaxVoltage:  0.0,
				hvRailHistory:       make([]timestampedVoltage, 0, voltageHistorySize),
				lvRailHistory:       make([]timestampedVoltage, 0, voltageHistorySize),
				historicalAverages:  make(map[string]float32),
				maxHistoryHours:     batteryConfig.DepletionHistoryHours,
			}

			// Process the voltage reading
			status := monitor.ProcessReading(tc.voltage, 0, 3.2)

			// Log the result
			t.Logf("Voltage %.2fV detected as: %s %d cells", tc.voltage, status.Chemistry, status.CellCount)
			t.Logf("Reason: %s", tc.description)

			// Verify the detection
			assert.Equal(t, tc.expectedChemistry, status.Chemistry,
				"Chemistry detection failed for %.2fV", tc.voltage)
			assert.Equal(t, tc.expectedCells, status.CellCount,
				"Cell count detection failed for %.2fV", tc.voltage)

			// Verify the pack was created correctly
			require.NotNil(t, monitor.currentPack, "Battery pack should be created")
			assert.Equal(t, tc.expectedChemistry, monitor.currentPack.Type.Chemistry)
			assert.Equal(t, tc.expectedCells, monitor.currentPack.CellCount)

			// Verify voltage is within the detected pack's range
			minV := monitor.currentPack.GetScaledMinVoltage()
			maxV := monitor.currentPack.GetScaledMaxVoltage()
			assert.GreaterOrEqual(t, tc.voltage, minV,
				"Voltage %.2fV should be >= min voltage %.2fV", tc.voltage, minV)
			assert.LessOrEqual(t, tc.voltage, maxV,
				"Voltage %.2fV should be <= max voltage %.2fV", tc.voltage, maxV)
		})
	}
}

// TestImmediateDetection verifies that detection happens on first reading
func TestImmediateDetection(t *testing.T) {
	stateDir := t.TempDir()

	// Initialize battery monitor
	batteryConfig := goconfig.DefaultBattery()

	monitor := &BatteryMonitor{
		config:              &batteryConfig,
		voltageHistory:      make([]timestampedVoltage, 0, voltageHistorySize),
		lastReportedPercent: -1,
		stateFilePath:       filepath.Join(stateDir, "battery_state.json"),
		dischargeRateAlpha:  0.1,
		dischargeRateWindow: make([]float32, 0, 20),
		lastDisplayedHours:  -1,
		observedMinVoltage:  999.0,
		observedMaxVoltage:  0.0,
		hvRailHistory:       make([]timestampedVoltage, 0, voltageHistorySize),
		lvRailHistory:       make([]timestampedVoltage, 0, voltageHistorySize),
		historicalAverages:  make(map[string]float32),
		maxHistoryHours:     batteryConfig.DepletionHistoryHours,
	}

	// Process first reading - should detect immediately
	voltage := float32(12.5)
	status := monitor.ProcessReading(voltage, 0, 3.2)

	// Should have detected battery on first reading
	assert.NotEqual(t, "unknown", status.Chemistry, "Should detect chemistry on first reading")
	assert.Greater(t, status.CellCount, 0, "Should detect cell count on first reading")
	assert.NotContains(t, status.Error, "collecting voltage data", "Should not wait for multiple readings")

	t.Logf("First reading %.2fV immediately detected as: %s %d cells",
		voltage, status.Chemistry, status.CellCount)
}


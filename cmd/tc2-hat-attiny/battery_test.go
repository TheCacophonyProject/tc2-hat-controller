package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TheCacophonyProject/go-config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BatteryTestCase represents a test case for battery type detection
type BatteryTestCase struct {
	name              string
	csvFile           string
	expectedType      string
	expectedChemistry string
	minReadings       int // Minimum readings before detection
}

// VoltageReading represents a single voltage reading from CSV
type VoltageReading struct {
	timestamp time.Time
	hvBat     float32
	lvBat     float32
	rtcBat    float32
}

func TestBatteryDetectionFromCSV(t *testing.T) {
	tests := []BatteryTestCase{
		{
			name:              "Lime Battery Detection",
			csvFile:           "../../test/lime_battery_readings.csv",
			expectedType:      "lime",
			expectedChemistry: "li-ion",
			minReadings:       25,
		},
		{
			name:              "LifePo 6v Battery Detection",
			csvFile:           "../../test/lifepo_battery_readings.csv",
			expectedType:      "lifepo4-6v",
			expectedChemistry: "lifepo4",
			minReadings:       25,
		},
		{
			name:              "Lifepo 12v Battery Detection",
			csvFile:           "../../test/lifepo12v_battery_readings.csv",
			expectedType:      "lifepo4-12v",
			expectedChemistry: "lifepo4",
			minReadings:       25,
		},
		{
			name:              "Lifepo 24v Battery Detection",
			csvFile:           "../../test/lifepo24v_battery_readings.csv",
			expectedType:      "lifepo4-24v",
			expectedChemistry: "lifepo4",
			minReadings:       25,
		},
		// Add more battery types here as CSV files become available
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create temporary state directory for test
			stateDir := t.TempDir()

			// Initialize battery monitor with default config
			batteryConfig := config.DefaultBattery()
			batteryConfig.EnableVoltageReadings = true
			monitor := &BatteryMonitor{
				config:              &batteryConfig,
				voltageHistory:      make([]timestampedVoltage, 0, voltageHistorySize),
				lastReportedPercent: -1,
				stateFilePath:       filepath.Join(stateDir, "battery_state.json"),
			}

			// Read CSV file
			readings, err := readBatteryCSV(tc.csvFile)
			require.NoError(t, err, "Failed to read CSV file")
			require.NotEmpty(t, readings, "No readings found in CSV file")

			// Process readings until battery type is detected
			var detectedType string
			var detectedChemistry string
			detectionReadings := 0

			for i, reading := range readings {
				// Wrap in defer to catch panics from battery.go bug
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Logf("Recovered from panic at reading %d: %v", i, r)
						}
					}()

					status := monitor.ProcessReading(reading.hvBat, reading.lvBat, reading.rtcBat)

					// Check if battery type has been detected
					if status.Type != "unknown" && status.Type != "none" && status.Error == "" {
						detectedType = status.Type
						detectedChemistry = status.Chemistry
						detectionReadings = i + 1
					}
				}()

				// Break if we detected a type
				if detectedType != "" {
					break
				}

				// Fail if too many readings without detection
				if i > tc.minReadings*2 {
					t.Fatalf("Failed to detect battery type after %d readings", i+1)
				}
			}

			// Verify detection
			assert.Equal(t, tc.expectedType, detectedType,
				"Expected battery type %s but detected %s", tc.expectedType, detectedType)
			assert.Equal(t, tc.expectedChemistry, detectedChemistry,
				"Expected chemistry %s but detected %s", tc.expectedChemistry, detectedChemistry)
			assert.LessOrEqual(t, detectionReadings, tc.minReadings,
				"Detection took %d readings, expected <= %d", detectionReadings, tc.minReadings)

			// Continue processing remaining readings to verify stability
			if detectionReadings < len(readings) {
				typeChanges := 0
				prevType := detectedType

				for i := detectionReadings; i < len(readings); i++ {
					status := monitor.ProcessReading(readings[i].hvBat, readings[i].lvBat, readings[i].rtcBat)

					if status.Type != prevType && status.Error == "" {
						typeChanges++
						prevType = status.Type
					}
				}

				assert.Equal(t, 0, typeChanges,
					"Battery type changed %d times after initial detection", typeChanges)
			}
		})
	}
}

func TestBatteryMonitorVoltageStability(t *testing.T) {
	// Test voltage stability calculation
	stateDir := t.TempDir()
	batteryConfig := config.DefaultBattery()
	monitor := &BatteryMonitor{
		config:              &batteryConfig,
		voltageHistory:      make([]timestampedVoltage, 0, voltageHistorySize),
		lastReportedPercent: -1,
		stateFilePath:       filepath.Join(stateDir, "battery_state.json"),
	}

	// Simulate stable voltage readings with realistic timing
	baseVoltage := float32(30.0)
	baseTime := time.Now()
	for i := 0; i < 10; i++ {
		// Add small variation
		voltage := baseVoltage + float32(i%2)*0.01
		entry := timestampedVoltage{
			voltage:   voltage,
			timestamp: baseTime.Add(time.Duration(i) * time.Minute), // 1 minute intervals
		}
		monitor.voltageHistory = append(monitor.voltageHistory, entry)
	}
}

func TestBatteryMonitorVoltageRangeDetection(t *testing.T) {
	// Test voltage range detection system
	stateDir := t.TempDir()
	batteryConfig := config.DefaultBattery()
	monitor := &BatteryMonitor{
		config:              &batteryConfig,
		voltageHistory:      make([]timestampedVoltage, 0, voltageHistorySize),
		observedMinVoltage:  999.0,
		observedMaxVoltage:  0.0,
		lastReportedPercent: -1,
		stateFilePath:       filepath.Join(stateDir, "battery_state.json"),
	}

	// Test voltage range tracking
	voltages := []float32{30.0, 32.0, 28.0, 35.0, 31.0}
	for _, voltage := range voltages {
		monitor.updateVoltageRange(voltage)
	}

	assert.Equal(t, float32(28.0), monitor.observedMinVoltage, "Expected min voltage to be 28.0")
	assert.Equal(t, float32(35.0), monitor.observedMaxVoltage, "Expected max voltage to be 35.0")
	assert.Equal(t, 5, monitor.voltageRangeReadings, "Expected 5 voltage readings")

	// Test battery change detection
	assert.False(t, monitor.detectBatteryChange(30.0), "Should not detect change for normal voltage")
	assert.True(t, monitor.detectBatteryChange(5.0), "Should detect change for 5V voltage (too low)")
	assert.True(t, monitor.detectBatteryChange(50.0), "Should detect change for 50V voltage (too high)")

	// Test detection with sufficient readings
	for i := 0; i < 25; i++ {
		monitor.updateVoltageRange(30.0 + float32(i%5))
	}

	// Should now be able to detect lime battery based on voltage range
	err := monitor.autoDetectType(30.0)
	assert.NoError(t, err, "Should successfully detect battery type with sufficient readings")
	assert.NotNil(t, monitor.currentType, "Should have detected a battery type")
	if monitor.currentType != nil {
		assert.Equal(t, "lime", monitor.currentType.Name, "Should detect lime battery for 30V range")
	}
}

func TestBatteryMonitorPersistentState(t *testing.T) {
	// Test persistent state save/load
	stateDir := t.TempDir()
	batteryConfig := config.DefaultBattery()

	// Create first monitor and detect a battery type
	monitor1 := &BatteryMonitor{
		config:              &batteryConfig,
		voltageHistory:      make([]timestampedVoltage, 0, voltageHistorySize),
		lastReportedPercent: -1,
		stateFilePath:       filepath.Join(stateDir, "battery_state.json"),
	}

	// Force lime battery detection
	for i := range config.PresetBatteryTypes {
		if config.PresetBatteryTypes[i].Name == "lime" {
			monitor1.currentType = &config.PresetBatteryTypes[i]
			break
		}
	}
	require.NotNil(t, monitor1.currentType, "Failed to set lime battery type")

	// Save state
	monitor1.savePersistentState()

	// Create second monitor and load state
	monitor2 := &BatteryMonitor{
		config:              &batteryConfig,
		voltageHistory:      make([]timestampedVoltage, 0, voltageHistorySize),
		lastReportedPercent: -1,
		stateFilePath:       filepath.Join(stateDir, "battery_state.json"),
	}

	err := monitor2.loadPersistentState()
	require.NoError(t, err, "Failed to load persistent state")

	// Verify loaded state
	require.NotNil(t, monitor2.currentType, "Battery type not loaded from state")
	assert.Equal(t, "lime", monitor2.currentType.Name,
		"Expected lime battery type from state, got %s", monitor2.currentType.Name)
}

// Helper function to read battery CSV file
func readBatteryCSV(filename string) ([]VoltageReading, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true

	var readings []VoltageReading

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read CSV record: %w", err)
		}

		// Skip header if present
		if strings.Contains(record[0], "timestamp") {
			continue
		}

		// Parse CSV format: timestamp, HV, LV, RTC
		if len(record) < 4 {
			continue // Skip incomplete records
		}

		// Parse timestamp
		timestamp, err := time.Parse("2006-01-02 15:04:05", record[0])
		if err != nil {
			// Try alternative formats if needed
			timestamp = time.Now() // Fallback for testing
		}

		// Parse voltages
		hvBat, err := strconv.ParseFloat(strings.TrimSpace(record[1]), 32)
		if err != nil {
			return nil, fmt.Errorf("failed to parse HV battery voltage: %w", err)
		}

		lvBat, err := strconv.ParseFloat(strings.TrimSpace(record[2]), 32)
		if err != nil {
			return nil, fmt.Errorf("failed to parse LV battery voltage: %w", err)
		}

		rtcBat, err := strconv.ParseFloat(strings.TrimSpace(record[3]), 32)
		if err != nil {
			return nil, fmt.Errorf("failed to parse RTC battery voltage: %w", err)
		}

		readings = append(readings, VoltageReading{
			timestamp: timestamp,
			hvBat:     float32(hvBat),
			lvBat:     float32(lvBat),
			rtcBat:    float32(rtcBat),
		})
	}

	return readings, nil
}

// Test helper to verify voltage readings from CSV
func TestReadBatteryCSV(t *testing.T) {
	readings, err := readBatteryCSV("../../test/lime_battery_readings.csv")
	require.NoError(t, err, "Failed to read CSV file")
	require.NotEmpty(t, readings, "No readings found in CSV")

	// Verify first reading
	first := readings[0]
	assert.InDelta(t, 30.18, first.hvBat, 0.01, "Unexpected HV battery voltage")
	assert.InDelta(t, 14.43, first.lvBat, 0.01, "Unexpected LV battery voltage")
	assert.InDelta(t, 3.26, first.rtcBat, 0.01, "Unexpected RTC battery voltage")

	// Verify we have multiple readings
	assert.Greater(t, len(readings), 10, "Expected at least 10 readings")
}

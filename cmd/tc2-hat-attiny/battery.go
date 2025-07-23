package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	goconfig "github.com/TheCacophonyProject/go-config"
	"github.com/godbus/dbus"
)

const (
	// History tracking constants
	voltageHistorySize = 10 // More samples for better chemistry detection
	stabilityWindow    = 5  // Window for stability calculation

	// Event reporting
	percentChangeThreshold = 5.0 // Report events on 5% change

	// State persistence
	stateFileName = "battery_state.json"
)

// BatteryStatus represents the complete battery state
type BatteryStatus struct {
	Voltage              float32            `json:"voltage"`
	Percent              float32            `json:"percent"`
	Type                 string             `json:"type"`
	Chemistry            string             `json:"chemistry"`
	Rail                 string             `json:"rail"`
	LastUpdated          time.Time          `json:"last_updated"`
	Error                string             `json:"error,omitempty"`
	DepletionEstimate    *DepletionEstimate `json:"depletion_estimate,omitempty"`
	DischargeRatePerHour float32            `json:"discharge_rate_per_hour"`
	ChargingDetected     bool               `json:"charging_detected"`
}

// BatteryMonitor manages stateful battery monitoring
type BatteryMonitor struct {
	config               *goconfig.Battery
	currentType          *goconfig.BatteryType
	voltageHistory       []timestampedVoltage
	observedMinVoltage   float32
	observedMaxVoltage   float32
	voltageRangeReadings int
	lastBatteryType      string

	// Rail tracking for dynamic detection
	hvRailHistory             []timestampedVoltage
	lvRailHistory             []timestampedVoltage
	activeRail                string
	railDeterminationReadings int

	lastReportedPercent float32
	lastValidStatus     *BatteryStatus
	stateFilePath       string
	rtcVoltage          float32

	// Discharge tracking
	dischargeHistory     []DischargeRateHistory
	dischargeStats       DischargeStatistics
	lastChargeEvent      time.Time
	historicalAverages   map[string]float32
	maxHistoryHours      int
	lastDepletionWarning time.Time
}

// timestampedVoltage holds voltage with timestamp for stability calculation
type timestampedVoltage struct {
	voltage   float32
	timestamp time.Time
}

// PersistentState represents the saved battery state
type PersistentState struct {
	DetectedType         string    `json:"detected_type,omitempty"`
	DetectedChemistry    string    `json:"detected_chemistry,omitempty"`
	ObservedMinVoltage   float32   `json:"observed_min_voltage"`
	ObservedMaxVoltage   float32   `json:"observed_max_voltage"`
	VoltageRangeReadings int       `json:"voltage_range_readings"`
	ActiveRail           string    `json:"active_rail,omitempty"`
	LastUpdated          time.Time `json:"last_updated"`

	// Discharge tracking
	DischargeHistory   []DischargeRateHistory `json:"discharge_history"`
	DischargeStats     DischargeStatistics    `json:"discharge_stats"`
	LastChargeEvent    time.Time              `json:"last_charge_event,omitempty"`
	PowerSavingActive  bool                   `json:"power_saving_active"`
	HistoricalAverages map[string]float32     `json:"historical_averages"` // By battery type
}

// DischargeRateHistory tracks discharge patterns
type DischargeRateHistory struct {
	Timestamp time.Time `json:"timestamp"`
	Voltage   float32   `json:"voltage"`
	Percent   float32   `json:"percent"`
}

// DischargeStatistics holds calculated discharge metrics
type DischargeStatistics struct {
	ShortTermRate  float32   `json:"short_term_rate"`  // %/hour over 30 min
	MediumTermRate float32   `json:"medium_term_rate"` // %/hour over 6 hours
	LongTermRate   float32   `json:"long_term_rate"`   // %/hour over 24 hours
	AverageRate    float32   `json:"average_rate"`     // Weighted average
	Confidence     float32   `json:"confidence"`       // 0-100%
	DataPoints     int       `json:"data_points"`
	LastUpdated    time.Time `json:"last_updated"`
}

// DepletionEstimate represents time till depletion estimate
type DepletionEstimate struct {
	EstimatedHours     float32   `json:"estimated_hours"`
	EstimatedDepletion time.Time `json:"estimated_depletion"`
	Confidence         float32   `json:"confidence"`
	Method             string    `json:"method"`        // "short_term", "averaged", "historical"
	WarningLevel       string    `json:"warning_level"` // "normal", "low", "critical"
}

// NewBatteryMonitor creates a new battery monitor instance
func NewBatteryMonitor(config *goconfig.Config, stateDir string) (*BatteryMonitor, error) {
	batteryConfig := goconfig.DefaultBattery()
	if err := config.Unmarshal(goconfig.BatteryKey, &batteryConfig); err != nil {
		return nil, fmt.Errorf("failed to load battery config: %w", err)
	}

	if !batteryConfig.EnableVoltageReadings {
		return nil, fmt.Errorf("battery voltage readings disabled")
	}

	monitor := &BatteryMonitor{
		config:              &batteryConfig,
		voltageHistory:      make([]timestampedVoltage, 0, voltageHistorySize),
		observedMinVoltage:  999.0, // Initialize to high value
		observedMaxVoltage:  0.0,   // Initialize to low value
		hvRailHistory:       make([]timestampedVoltage, 0, voltageHistorySize),
		lvRailHistory:       make([]timestampedVoltage, 0, voltageHistorySize),
		lastReportedPercent: -1,
		stateFilePath:       filepath.Join(stateDir, stateFileName),
		dischargeHistory:    make([]DischargeRateHistory, 0),
		historicalAverages:  make(map[string]float32),
		maxHistoryHours:     batteryConfig.DepletionHistoryHours,
	}

	// Load persistent state if available
	if err := monitor.loadPersistentState(); err != nil {
		log.Printf("Could not load persistent battery state: %v", err)
	}

	// Load configured type
	monitor.loadConfiguredType()

	return monitor, nil
}

// ProcessReading processes new voltage readings and returns battery status
func (m *BatteryMonitor) ProcessReading(hvBat, lvBat, rtcBat float32) *BatteryStatus {
	m.rtcVoltage = rtcBat

	// Select voltage source
	voltage, rail := m.determineActiveRail(hvBat, lvBat)

	// Check minimum detection threshold
	if voltage < m.config.MinimumVoltageDetection {
		return &BatteryStatus{
			Voltage:     voltage,
			Percent:     -1,
			Type:        "none",
			Chemistry:   "unknown",
			Rail:        rail,
			Error:       fmt.Sprintf("voltage %.2fV below detection threshold", voltage),
			LastUpdated: time.Now(),
		}
	}

	// Update voltage history
	m.addToHistory(voltage)

	// Handle battery type detection/validation
	if err := m.ensureBatteryType(voltage); err != nil {
		status := &BatteryStatus{
			Voltage:     voltage,
			Percent:     -1,
			Type:        "unknown",
			Chemistry:   "unknown",
			Rail:        rail,
			Error:       err.Error(),
			LastUpdated: time.Now(),
		}

		// Use last valid percentage if available
		if m.lastValidStatus != nil {
			status.Percent = m.lastValidStatus.Percent
			status.Type = m.lastValidStatus.Type
			status.Chemistry = m.lastValidStatus.Chemistry
		}

		return status
	}

	// Calculate percentage
	percent, err := m.calculatePercent(voltage)
	if err != nil {
		return &BatteryStatus{
			Voltage:     voltage,
			Percent:     -1,
			Type:        m.currentType.Name,
			Chemistry:   m.currentType.Chemistry,
			Rail:        rail,
			Error:       err.Error(),
			LastUpdated: time.Now(),
		}
	}

	// Create successful status
	status := &BatteryStatus{
		Voltage:     voltage,
		Percent:     percent,
		Type:        m.currentType.Name,
		Chemistry:   m.currentType.Chemistry,
		Rail:        rail,
		LastUpdated: time.Now(),
	}

	// Update discharge history if depletion estimation is enabled
	if m.config.EnableDepletionEstimate {
		m.UpdateDischargeHistory(status)

		// Get depletion estimate if we have data
		if len(m.dischargeHistory) > 1 {
			status.DepletionEstimate = m.GetDepletionEstimate()
			if m.dischargeStats.AverageRate > 0 {
				status.DischargeRatePerHour = m.dischargeStats.AverageRate
			}
		}
	}

	// Check for charging
	if m.lastValidStatus != nil {
		status.ChargingDetected = m.DetectChargingEvent(voltage, m.lastValidStatus.Voltage,
			percent, m.lastValidStatus.Percent)
	}

	m.lastValidStatus = status
	return status
}

// determineActiveRail determines which rail is active based on voltage variation
func (m *BatteryMonitor) determineActiveRail(hvBat, lvBat float32) (float32, string) {
	// Add current readings to rail histories
	now := time.Now()
	m.addToRailHistory(hvBat, lvBat, now)

	// If we already determined an active rail and have confidence, use it
	if m.activeRail != "" && m.railDeterminationReadings >= 10 {
		// Check if the determined rail still has reasonable voltage
		if m.activeRail == goconfig.RailHV && hvBat > 1.0 {
			return hvBat, goconfig.RailHV
		}
		if m.activeRail == goconfig.RailLV && lvBat > 1.0 {
			return lvBat, goconfig.RailLV
		}
		// If the previously active rail dropped too low, re-evaluate
	}

	// Need enough readings to determine rail activity
	if m.railDeterminationReadings < 5 {
		// Not enough data yet, use simple heuristic
		if hvBat > 2.0 && hvBat > lvBat+1.0 {
			return hvBat, goconfig.RailHV
		}
		if lvBat > 2.0 {
			return lvBat, goconfig.RailLV
		}
		return hvBat, goconfig.RailHV // Default to HV if both are low
	}

	// Calculate activity scores for each rail
	hvActivity := m.calculateRailActivity(m.hvRailHistory)
	lvActivity := m.calculateRailActivity(m.lvRailHistory)

	// Determine active rail based on activity and voltage levels
	var selectedRail string
	var selectedVoltage float32

	if hvActivity > lvActivity+0.1 && hvBat > 1.0 { // HV has more activity
		selectedRail = goconfig.RailHV
		selectedVoltage = hvBat
	} else if lvActivity > hvActivity+0.1 && lvBat > 1.0 { // LV has more activity
		selectedRail = goconfig.RailLV
		selectedVoltage = lvBat
	} else {
		// Similar activity or no clear winner, use voltage level
		if hvBat > lvBat+1.0 && hvBat > 2.0 {
			selectedRail = goconfig.RailHV
			selectedVoltage = hvBat
		} else if lvBat > 2.0 {
			selectedRail = goconfig.RailLV
			selectedVoltage = lvBat
		} else {
			selectedRail = goconfig.RailHV // Default
			selectedVoltage = hvBat
		}
	}

	// Update active rail if it changed
	if m.activeRail != selectedRail {
		log.Printf("Switching active rail from %s to %s (HV activity: %.3f, LV activity: %.3f)",
			m.activeRail, selectedRail, hvActivity, lvActivity)
		m.activeRail = selectedRail
	}

	return selectedVoltage, selectedRail
}

func (m *BatteryMonitor) ensureBatteryType(voltage float32) error {
	// Update voltage range tracking
	m.updateVoltageRange(voltage)

	// Check for battery change
	if m.detectBatteryChange(voltage) {
		log.Printf("Battery change detected at voltage %.2fV", voltage)
		m.resetDetection()
		m.savePersistentState()
	}

	// If we have a type, validate it's still in range
	if m.currentType != nil {
		if voltage >= m.currentType.MinVoltage && voltage <= m.currentType.MaxVoltage {
			return nil
		}
		// Voltage out of range, but could be normal discharge/charge
		// Don't immediately invalidate unless it's a significant change
	}

	return m.autoDetectType(voltage)
}

// autoDetectType attempts to detect battery type from voltage ranges
func (m *BatteryMonitor) autoDetectType(voltage float32) error {
	// Need sufficient readings before attempting detection
	if m.voltageRangeReadings < 5 {
		return fmt.Errorf("collecting voltage data (%d/5 readings)", m.voltageRangeReadings)
	}

	// For immediate detection, check if current voltage matches any preset
	var immediateMatch *goconfig.BatteryType
	for i := range goconfig.PresetBatteryTypes {
		preset := &goconfig.PresetBatteryTypes[i]
		preset.NormalizeCurves()

		if voltage >= preset.MinVoltage && voltage <= preset.MaxVoltage {
			if immediateMatch == nil {
				immediateMatch = preset
			} else {
				// If multiple matches, prefer based on priority:
				// 1. Narrower voltage range (more specific)
				// 2. LiFePO4 chemistry (tends to be more stable)
				presetRange := preset.MaxVoltage - preset.MinVoltage
				currentRange := immediateMatch.MaxVoltage - immediateMatch.MinVoltage

				if presetRange < currentRange {
					immediateMatch = preset
				} else if presetRange == currentRange && preset.Chemistry == goconfig.ChemistryLiFePO4 && immediateMatch.Chemistry != goconfig.ChemistryLiFePO4 {
					immediateMatch = preset
				}
			}
		}
	}

	// If we have enough range data, use it for more accurate detection
	if m.voltageRangeReadings >= 20 {
		var bestMatch *goconfig.BatteryType
		var bestScore float32

		for i := range goconfig.PresetBatteryTypes {
			preset := &goconfig.PresetBatteryTypes[i]
			preset.NormalizeCurves()

			// Check if observed range overlaps significantly with preset range
			tolerance := float32(1.0) // 1V tolerance

			// Check for overlap: observed range should fit within or overlap preset range
			overlapMin := float32(math.Max(float64(m.observedMinVoltage), float64(preset.MinVoltage-tolerance)))
			overlapMax := float32(math.Min(float64(m.observedMaxVoltage), float64(preset.MaxVoltage+tolerance)))

			if overlapMax > overlapMin {
				// Calculate overlap percentage
				observedRange := m.observedMaxVoltage - m.observedMinVoltage
				overlapRange := overlapMax - overlapMin
				score := overlapRange / observedRange

				if score > bestScore {
					bestScore = score
					bestMatch = preset
				}
			}
		}

		if bestMatch != nil {
			m.currentType = bestMatch
			log.Printf("Auto-detected battery: %s (%s chemistry) based on voltage range %.2f-%.2fV",
				m.currentType.Name, m.currentType.Chemistry,
				m.observedMinVoltage, m.observedMaxVoltage)
			m.lastBatteryType = m.currentType.Name
			m.savePersistentState()
			return nil
		}
	}

	// Use immediate match if available and we don't have a better range match yet
	if immediateMatch != nil {
		// If we already have a type and it's the same, stick with it
		if m.currentType != nil && m.currentType.Name == immediateMatch.Name {
			return nil
		}

		// Use immediate match for initial detection
		if m.currentType == nil {
			m.currentType = immediateMatch
			log.Printf("Auto-detected battery: %s (%s chemistry) based on current voltage %.2fV",
				m.currentType.Name, m.currentType.Chemistry, voltage)
			m.lastBatteryType = m.currentType.Name
			m.savePersistentState()
			return nil
		}
	}

	return fmt.Errorf("no battery type matches observed range %.2f-%.2fV",
		m.observedMinVoltage, m.observedMaxVoltage)
}

func (m *BatteryMonitor) addToHistory(voltage float32) {
	entry := timestampedVoltage{
		voltage:   voltage,
		timestamp: time.Now(),
	}

	m.voltageHistory = append(m.voltageHistory, entry)
	if len(m.voltageHistory) > voltageHistorySize {
		m.voltageHistory = m.voltageHistory[1:]
	}
}

// updateVoltageRange tracks the min/max voltage seen over time
func (m *BatteryMonitor) updateVoltageRange(voltage float32) {
	// Initialize range on first reading
	if m.voltageRangeReadings == 0 {
		m.observedMinVoltage = voltage
		m.observedMaxVoltage = voltage
	} else {
		if voltage < m.observedMinVoltage {
			m.observedMinVoltage = voltage
		}
		if voltage > m.observedMaxVoltage {
			m.observedMaxVoltage = voltage
		}
	}
	m.voltageRangeReadings++
}

// detectBatteryChange checks for sudden voltage changes indicating battery swap
func (m *BatteryMonitor) detectBatteryChange(voltage float32) bool {
	// Don't detect changes until we have some history
	if m.voltageRangeReadings < 5 {
		return false
	}

	// Check for sudden voltage jump (more than 2V change)
	if len(m.voltageHistory) > 0 {
		lastVoltage := m.voltageHistory[len(m.voltageHistory)-1].voltage
		if math.Abs(float64(voltage-lastVoltage)) > 2.0 {
			return true
		}
	}

	// Check if voltage is way outside previously observed range
	voltageRange := m.observedMaxVoltage - m.observedMinVoltage
	if voltageRange > 1.0 { // Only if we have a reasonable range
		if voltage < m.observedMinVoltage-1.0 || voltage > m.observedMaxVoltage+1.0 {
			return true
		}
	}

	return false
}

// resetDetection clears detection state for new battery
func (m *BatteryMonitor) resetDetection() {
	m.currentType = nil
	m.observedMinVoltage = 999.0
	m.observedMaxVoltage = 0.0
	m.voltageRangeReadings = 0
	m.lastBatteryType = ""
	m.voltageHistory = make([]timestampedVoltage, 0, voltageHistorySize)
	m.hvRailHistory = make([]timestampedVoltage, 0, voltageHistorySize)
	m.lvRailHistory = make([]timestampedVoltage, 0, voltageHistorySize)
	m.activeRail = ""
	m.railDeterminationReadings = 0
}

// addToRailHistory adds voltage readings to both rail histories
func (m *BatteryMonitor) addToRailHistory(hvBat, lvBat float32, timestamp time.Time) {
	hvEntry := timestampedVoltage{voltage: hvBat, timestamp: timestamp}
	lvEntry := timestampedVoltage{voltage: lvBat, timestamp: timestamp}

	m.hvRailHistory = append(m.hvRailHistory, hvEntry)
	if len(m.hvRailHistory) > voltageHistorySize {
		m.hvRailHistory = m.hvRailHistory[1:]
	}

	m.lvRailHistory = append(m.lvRailHistory, lvEntry)
	if len(m.lvRailHistory) > voltageHistorySize {
		m.lvRailHistory = m.lvRailHistory[1:]
	}

	m.railDeterminationReadings++
}

// calculateRailActivity calculates activity score for a rail based on voltage variation
func (m *BatteryMonitor) calculateRailActivity(history []timestampedVoltage) float32 {
	if len(history) < 3 {
		return 0.0
	}

	// Calculate standard deviation of voltages
	var sum, sumSquares float64
	count := len(history)

	for _, entry := range history {
		sum += float64(entry.voltage)
	}
	mean := sum / float64(count)

	for _, entry := range history {
		diff := float64(entry.voltage) - mean
		sumSquares += diff * diff
	}

	variance := sumSquares / float64(count)
	stdDev := math.Sqrt(variance)

	// Calculate voltage range
	minVoltage := history[0].voltage
	maxVoltage := history[0].voltage
	for _, entry := range history {
		if entry.voltage < minVoltage {
			minVoltage = entry.voltage
		}
		if entry.voltage > maxVoltage {
			maxVoltage = entry.voltage
		}
	}
	voltageRange := maxVoltage - minVoltage

	// Activity score combines standard deviation and range
	// Higher values indicate more voltage variation (active battery)
	activityScore := float32(stdDev*100 + float64(voltageRange)*50)

	// Bonus for having meaningful voltage levels
	if mean > 2.0 {
		activityScore += 10.0
	}

	return activityScore
}

func (m *BatteryMonitor) calculatePercent(voltage float32) (float32, error) {
	if m.currentType == nil {
		return -1, fmt.Errorf("no battery type detected")
	}

	voltages := m.currentType.Voltages
	percents := m.currentType.Percent

	// Validate curves
	if len(voltages) != len(percents) || len(voltages) == 0 {
		return -1, fmt.Errorf("invalid voltage/percent curves for %s", m.currentType.Name)
	}

	// Handle boundary conditions
	if voltage <= voltages[0] {
		return percents[0], nil
	}
	if voltage >= voltages[len(voltages)-1] {
		return percents[len(percents)-1], nil
	}

	// Binary search for interpolation interval
	left, right := 0, len(voltages)-1
	for left < right-1 {
		mid := (left + right) / 2
		if voltage < voltages[mid] {
			right = mid
		} else {
			left = mid
		}
	}

	// Linear interpolation
	v1, v2 := voltages[left], voltages[right]
	p1, p2 := percents[left], percents[right]

	if v2 == v1 {
		return p1, nil // Avoid division by zero
	}

	percent := p1 + (p2-p1)*(voltage-v1)/(v2-v1)

	// Ensure result is within bounds
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}

	return percent, nil
}

func (m *BatteryMonitor) ShouldReportEvent(status *BatteryStatus) bool {
	if status.Error != "" {
		return false // Don't report events for errors
	}

	// Report on significant percentage change
	if m.lastReportedPercent < 0 ||
		math.Abs(float64(status.Percent-m.lastReportedPercent)) >= percentChangeThreshold {
		m.lastReportedPercent = status.Percent
		return true
	}

	// Report on chemistry detection
	if m.lastValidStatus != nil && m.lastValidStatus.Type != status.Type {
		return true
	}

	return false
}

func (m *BatteryMonitor) GetRTCVoltage() float32 {
	return m.rtcVoltage
}

func (m *BatteryMonitor) loadConfiguredType() {
	configuredType := m.config.GetBatteryType()
	if configuredType != nil {
		m.currentType = configuredType
		log.Printf("Using configured battery type: %s (%s chemistry)",
			m.currentType.Name, m.currentType.Chemistry)
		m.savePersistentState()
	}
}

func (m *BatteryMonitor) loadPersistentState() error {
	data, err := os.ReadFile(m.stateFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No state file yet
		}
		return err
	}

	var state PersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	// Restore voltage range data
	if state.ObservedMinVoltage > 0 && state.ObservedMaxVoltage > 0 {
		m.observedMinVoltage = state.ObservedMinVoltage
		m.observedMaxVoltage = state.ObservedMaxVoltage
		m.voltageRangeReadings = state.VoltageRangeReadings
	}

	// Restore active rail
	if state.ActiveRail != "" {
		m.activeRail = state.ActiveRail
		m.railDeterminationReadings = 10 // Assume we have confidence
	}

	// Restore discharge tracking
	m.dischargeHistory = state.DischargeHistory
	m.dischargeStats = state.DischargeStats
	m.lastChargeEvent = state.LastChargeEvent
	if state.HistoricalAverages != nil {
		m.historicalAverages = state.HistoricalAverages
	}

	// Only use saved state if no configured type and state is recent
	if m.currentType == nil && state.DetectedType != "" &&
		time.Since(state.LastUpdated) < 24*time.Hour {
		for i := range goconfig.PresetBatteryTypes {
			preset := &goconfig.PresetBatteryTypes[i]
			if preset.Name == state.DetectedType {
				preset.NormalizeCurves()
				m.currentType = preset
				m.lastBatteryType = preset.Name
				log.Printf("Restored battery type from state: %s (%s chemistry)",
					m.currentType.Name, m.currentType.Chemistry)
				break
			}
		}
	}

	return nil
}

func (m *BatteryMonitor) savePersistentState() {
	state := PersistentState{
		ObservedMinVoltage:   m.observedMinVoltage,
		ObservedMaxVoltage:   m.observedMaxVoltage,
		VoltageRangeReadings: m.voltageRangeReadings,
		ActiveRail:           m.activeRail,
		LastUpdated:          time.Now(),
		DischargeHistory:     m.dischargeHistory,
		DischargeStats:       m.dischargeStats,
		LastChargeEvent:      m.lastChargeEvent,
		HistoricalAverages:   m.historicalAverages,
	}

	if m.currentType != nil {
		state.DetectedType = m.currentType.Name
		state.DetectedChemistry = m.currentType.Chemistry
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("Failed to marshal battery state: %v", err)
		return
	}

	if err := os.WriteFile(m.stateFilePath, data, 0644); err != nil {
		log.Printf("Failed to save battery state: %v", err)
	}
}

// monitorVoltageLoop monitors battery voltage and reports status
func monitorVoltageLoop(a *attiny, config *goconfig.Config) {
	// Initialize battery monitor
	stateDir := "/var/lib/tc2-hat-controller" // Or use appropriate state directory
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		log.Printf("Failed to create state directory: %v", err)
		return
	}

	batteryMonitor, err := NewBatteryMonitor(config, stateDir)
	if err != nil {
		log.Printf("Failed to initialize battery monitor: %v", err)
		return
	}

	// Truncate battery readings file if needed
	err = keepLastLines(batteryReadingsFile, batteryMaxLines)
	if err != nil {
		log.Printf("Could not truncate battery readings file: %v", err)
	}

	startTime := time.Now()
	logCounter := 5

	for {
		// Read voltage values from ATtiny
		hvBat, err := a.readHVBattery()
		if err != nil {
			log.Error("Error reading HV battery:", err)
			time.Sleep(2 * time.Minute)
			continue
		}

		lvBat, err := a.readLVBattery()
		if err != nil {
			log.Error("Error reading LV battery:", err)
			time.Sleep(2 * time.Minute)
			continue
		}

		rtcBat, err := a.readRTCBattery()
		if err != nil {
			log.Error("Error reading RTC battery:", err)
			rtcBat = 0 // Continue without RTC voltage
		}

		// Process readings with new battery monitor
		status := batteryMonitor.ProcessReading(hvBat, lvBat, rtcBat)

		// Log to CSV file
		if err := logBatteryReadingToFile(hvBat, lvBat, rtcBat, status); err != nil {
			log.Error("Error logging battery reading:", err)
		}

		// Log to console periodically
		if logCounter >= 5 {
			if status.Error != "" {
				log.Printf("Battery reading: HV=%.2f, LV=%.2f, RTC=%.2f - Error: %s",
					hvBat, lvBat, rtcBat, status.Error)
			} else {
				log.Printf("Battery reading: HV=%.2f, LV=%.2f, RTC=%.2f - %s (%s) %.1f%% on %s rail, ",
					hvBat, lvBat, rtcBat, status.Type, status.Chemistry,
					status.Percent, status.Rail)
			}
			logCounter = 0
		}
		logCounter++

		// Truncate log file daily
		if time.Since(startTime) > 24*time.Hour {
			if err := keepLastLines(batteryReadingsFile, batteryMaxLines); err != nil {
				log.Printf("Could not truncate battery readings file: %v", err)
			} else {
				startTime = time.Now()
			}
		}

		// Report events if needed
		if batteryMonitor.ShouldReportEvent(status) {
			reportBatteryEvent(status, rtcBat)

			// Send D-Bus signal
			if err := sendBatterySignal(float64(status.Voltage), float64(status.Percent)); err != nil {
				log.Error("Error sending battery signal:", err)
			}
		}

		// Check for low battery conditions
		if status.Percent >= 0 && status.Percent <= 10 && status.Error == "" {
			log.Warnf("Low battery warning: %.1f%% (%s)", status.Percent, status.Type)
		}

		// Check for depletion warnings
		if status.DepletionEstimate != nil && status.DepletionEstimate.EstimatedHours > 0 {
			switch status.DepletionEstimate.WarningLevel {
			case "critical":
				log.Errorf("Critical battery depletion warning: %.1f hours remaining (%.0f%% confidence)",
					status.DepletionEstimate.EstimatedHours, status.DepletionEstimate.Confidence)
				reportDepletionWarningEvent(status, "critical")
			case "low":
				if time.Since(batteryMonitor.lastDepletionWarning) > 6*time.Hour {
					log.Warnf("Low battery runtime warning: %.1f hours remaining (%.0f%% confidence)",
						status.DepletionEstimate.EstimatedHours, status.DepletionEstimate.Confidence)
					reportDepletionWarningEvent(status, "low")
					batteryMonitor.lastDepletionWarning = time.Now()
				}
			}
		}

		time.Sleep(2 * time.Minute)
	}
}

// logBatteryReadingToFile logs battery readings to CSV file
func logBatteryReadingToFile(hvBat, lvBat, rtcBat float32, status *BatteryStatus) error {
	file, err := os.OpenFile(batteryReadingsFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	// Calculate depletion fields
	dischargeRate := status.DischargeRatePerHour
	hoursRemaining := float32(-1)
	confidence := float32(0)

	if status.DepletionEstimate != nil {
		hoursRemaining = status.DepletionEstimate.EstimatedHours
		confidence = status.DepletionEstimate.Confidence
	}

	// Format: timestamp, HV, LV, RTC, type, chemistry, percent, rail, error, discharge_rate, hours_remaining, confidence
	line := fmt.Sprintf("%s, %.2f, %.2f, %.2f, %s, %s, %.1f, %s, %s, %.2f, %.1f, %.1f",
		time.Now().Format("2006-01-02 15:04:05"),
		hvBat, lvBat, rtcBat,
		status.Type, status.Chemistry, status.Percent,
		status.Rail, status.Error,
		dischargeRate, hoursRemaining, confidence)

	_, err = file.WriteString(line + "\n")
	return err
}

// reportBatteryEvent reports battery status to event system
func reportBatteryEvent(status *BatteryStatus, rtcVoltage float32) {
	if status.Error != "" {
		// Don't report error states as normal battery events
		return
	}

	// Round percentage for event reporting
	roundedPercent := int(math.Round(float64(status.Percent)))

	// Build event details
	event := eventclient.Event{
		Timestamp: time.Now(),
		Type:      "rpiBattery",
		Details: map[string]interface{}{
			"battery":     roundedPercent,
			"batteryType": status.Type,
			"chemistry":   status.Chemistry,
			"voltage":     status.Voltage,
			"rail":        status.Rail,
			"rtcVoltage":  fmt.Sprintf("%.2f", rtcVoltage),
		},
	}

	err := eventclient.AddEvent(event)
	if err != nil {
		log.Error("Error sending battery event:", err)
	} else {
		log.Infof("Battery event: type=%s (%s), voltage=%.2fV, percent=%d%%",
			status.Type, status.Chemistry, status.Voltage, roundedPercent)
	}
}

// reportDepletionWarningEvent reports battery depletion warnings to event system
func reportDepletionWarningEvent(status *BatteryStatus, severity string) {
	if status.DepletionEstimate == nil {
		return
	}

	// Format time remaining for display
	hours := status.DepletionEstimate.EstimatedHours
	var timeRemaining string
	if hours > 24 {
		days := int(hours / 24)
		remainingHours := int(hours) % 24
		timeRemaining = fmt.Sprintf("%d days %d hours", days, remainingHours)
	} else if hours >= 1 {
		wholehours := int(hours)
		minutes := int((hours - float32(wholehours)) * 60)
		if hours >= 6 {
			timeRemaining = fmt.Sprintf("%d hours", wholehours)
		} else {
			timeRemaining = fmt.Sprintf("%d hours %d minutes", wholehours, minutes)
		}
	} else {
		minutes := int(hours * 60)
		timeRemaining = fmt.Sprintf("%d minutes", minutes)
	}

	event := eventclient.Event{
		Timestamp: time.Now(),
		Type:      "batteryDepletionWarning",
		Details: map[string]interface{}{
			"severity":       severity,
			"hoursRemaining": status.DepletionEstimate.EstimatedHours,
			"timeRemaining":  timeRemaining,
			"confidence":     status.DepletionEstimate.Confidence,
			"method":         status.DepletionEstimate.Method,
			"dischargeRate":  status.DischargeRatePerHour,
			"currentPercent": status.Percent,
			"batteryType":    status.Type,
		},
	}

	err := eventclient.AddEvent(event)
	if err != nil {
		log.Error("Error sending battery depletion warning event:", err)
	} else {
		log.Infof("Battery depletion warning event sent: %s - %s remaining", severity, timeRemaining)
	}
}

// sendBatterySignal sends battery status via D-Bus
func sendBatterySignal(voltage, percent float64) error {
	// Connect to the system bus
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}

	// Request a name on the bus (required for sending signals)
	const busName = "org.cacophony.attiny.Sender"
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("could not acquire bus name")
	}

	// Define the signal
	sig := &dbus.Signal{
		Path: dbus.ObjectPath("/org/cacophony/attiny"),
		Name: "org.cacophony.attiny.Battery",
		Body: []interface{}{voltage, percent},
	}

	// Emit the signal
	conn.Emit(sig.Path, sig.Name, sig.Body...)
	log.Printf("Emitted battery signal: voltage=%.2f, percent=%.2f", voltage, percent)

	return nil
}

// makeBatteryReadings is a debugging tool for reading battery voltages
func makeBatteryReadings(attiny *attiny) error {
	log.Info("Starting battery reading debug loop.")

	// Initialize battery monitor for testing
	config, err := goconfig.New(goconfig.DefaultConfigDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	stateDir := "/tmp/battery-debug"
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create debug state directory: %w", err)
	}

	batteryMonitor, err := NewBatteryMonitor(config, stateDir)
	if err != nil {
		return fmt.Errorf("failed to initialize battery monitor: %w", err)
	}

	readings := 60
	rawValues := make([]uint16, readings)
	rawDiffs := make([]uint16, readings)
	voltageValues := make([]float32, readings)
	statusHistory := make([]*BatteryStatus, 0, readings)

	log.Info("Collecting raw analog readings...")

	// Collect raw readings
	for i := 0; i < readings; i++ {
		rawValues[i], rawDiffs[i], err = attiny.readBattery(batteryHVDivVal1Reg, batteryHVDivVal2Reg)
		if err != nil {
			log.Error("Error reading battery:", err)
			continue
		}
		log.Infof("Raw reading %d/%d: value=%d, diff=%d", i+1, readings, rawValues[i], rawDiffs[i])
		time.Sleep(1 * time.Second)
	}

	// Calculate raw statistics
	rawSD := calculateStandardDeviation(rawValues)
	rawMean := calculateMean(rawValues)
	diffSD := calculateStandardDeviation(rawDiffs)
	diffMean := calculateMean(rawDiffs)

	log.Infof("Raw analog statistics - SD: %.2f, Mean: %.2f, Diff SD: %.2f, Diff Mean: %.2f",
		rawSD, rawMean, diffSD, diffMean)

	log.Info("\nCollecting voltage readings with battery monitoring...")

	// Collect voltage readings
	for i := 0; i < readings; i++ {
		hvBat, err := attiny.readHVBattery()
		if err != nil {
			log.Error("Error reading HV battery:", err)
			continue
		}

		lvBat, err := attiny.readLVBattery()
		if err != nil {
			log.Error("Error reading LV battery:", err)
			continue
		}

		rtcBat, err := attiny.readRTCBattery()
		if err != nil {
			log.Error("Error reading RTC battery:", err)
			rtcBat = 0
		}

		// Process with battery monitor
		status := batteryMonitor.ProcessReading(hvBat, lvBat, rtcBat)
		statusHistory = append(statusHistory, status)

		// Store voltage for statistics
		voltageValues[i] = status.Voltage

		// Log detailed reading
		if status.Error != "" {
			log.Infof("Reading %d/%d: HV=%.2fV, LV=%.2fV, RTC=%.2fV - Error: %s",
				i+1, readings, hvBat, lvBat, rtcBat, status.Error)
		} else {
			log.Infof("Reading %d/%d: HV=%.2fV, LV=%.2fV, RTC=%.2fV - %s (%s) %.1f%% on %s",
				i+1, readings, hvBat, lvBat, rtcBat,
				status.Type, status.Chemistry, status.Percent, status.Rail)
		}

		time.Sleep(1 * time.Second)
	}

	// Calculate voltage statistics
	voltSD := calculateStandardDeviationFloat32(voltageValues)
	voltMean := calculateMeanFloat32(voltageValues)

	log.Info("\n=== Battery Reading Summary ===")
	log.Infof("Raw Analog: Mean=%.2f, SD=%.2f", rawMean, rawSD)
	log.Infof("Voltage: Mean=%.2fV, SD=%.2fV", voltMean, voltSD)

	// Analyze detection results
	if len(statusHistory) > 0 {
		lastStatus := statusHistory[len(statusHistory)-1]
		if lastStatus.Error == "" {
			log.Infof("Detected Battery: %s (%s chemistry)", lastStatus.Type, lastStatus.Chemistry)

			// Count detection changes
			changes := 0
			prevType := ""
			for _, s := range statusHistory {
				if s.Type != prevType && prevType != "" {
					changes++
				}
				prevType = s.Type
			}
			log.Infof("Detection stability: %d type changes in %d readings", changes, len(statusHistory))
		}
	}

	return nil
}

func calculateStandardDeviationFloat32(values []float32) float32 {
	if len(values) == 0 {
		return 0
	}

	mean := calculateMeanFloat32(values)
	var sum float32

	for _, v := range values {
		diff := v - mean
		sum += diff * diff
	}

	variance := sum / float32(len(values))
	return float32(math.Sqrt(float64(variance)))
}

func calculateMeanFloat32(values []float32) float32 {
	if len(values) == 0 {
		return 0
	}

	var sum float32
	for _, v := range values {
		sum += v
	}

	return sum / float32(len(values))
}

// DetectChargingEvent checks if battery is being charged or swapped
func (m *BatteryMonitor) DetectChargingEvent(currentVoltage, previousVoltage float32, currentPercent, previousPercent float32) bool {
	// Check for voltage increase > 0.5V
	if currentVoltage > previousVoltage+0.5 {
		log.Printf("Charging event detected: voltage increased by %.2fV", currentVoltage-previousVoltage)
		return true
	}

	// Check for percentage increase > 5%
	if currentPercent > previousPercent+5.0 {
		log.Printf("Charging event detected: percentage increased by %.1f%%", currentPercent-previousPercent)
		return true
	}

	// Check if voltage jumped to near max for battery type
	if m.currentType != nil {
		maxThreshold := m.currentType.MaxVoltage - 0.5
		if currentVoltage >= maxThreshold && previousVoltage < maxThreshold-1.0 {
			log.Printf("Charging event detected: voltage jumped to near max (%.2fV)", currentVoltage)
			return true
		}
	}

	return false
}

// UpdateDischargeHistory adds new data point and manages history size
func (m *BatteryMonitor) UpdateDischargeHistory(status *BatteryStatus) {
	if status.Error != "" || status.Percent < 0 {
		return // Don't track invalid readings
	}

	// Check if this is a charging event
	if len(m.dischargeHistory) > 0 {
		lastEntry := m.dischargeHistory[len(m.dischargeHistory)-1]
		if m.DetectChargingEvent(status.Voltage, lastEntry.Voltage, status.Percent, lastEntry.Percent) {
			m.lastChargeEvent = time.Now()
			// Clear discharge history on charge event
			m.dischargeHistory = make([]DischargeRateHistory, 0)
			m.dischargeStats = DischargeStatistics{} // Reset stats
			log.Printf("Discharge history cleared due to charging event")
			return
		}
	}

	// Add new entry
	entry := DischargeRateHistory{
		Timestamp: status.LastUpdated,
		Voltage:   status.Voltage,
		Percent:   status.Percent,
	}

	m.dischargeHistory = append(m.dischargeHistory, entry)

	// Trim old entries (keep only maxHistoryHours worth)
	cutoffTime := time.Now().Add(-time.Duration(m.maxHistoryHours) * time.Hour)
	trimIndex := 0
	for i, e := range m.dischargeHistory {
		if e.Timestamp.After(cutoffTime) {
			trimIndex = i
			break
		}
	}
	if trimIndex > 0 {
		m.dischargeHistory = m.dischargeHistory[trimIndex:]
	}
}

// CalculateDischargeRate calculates discharge rate over specified duration
func (m *BatteryMonitor) CalculateDischargeRate(duration time.Duration) (float32, error) {
	if len(m.dischargeHistory) < 2 {
		return 0, fmt.Errorf("insufficient discharge history")
	}

	now := time.Now()
	cutoffTime := now.Add(-duration)

	// Find entries within the duration
	var startEntry, endEntry *DischargeRateHistory
	for i := range m.dischargeHistory {
		entry := &m.dischargeHistory[i]
		if entry.Timestamp.After(cutoffTime) {
			if startEntry == nil {
				// Look for an entry just before cutoff time if available
				if i > 0 {
					startEntry = &m.dischargeHistory[i-1]
				} else {
					startEntry = entry
				}
			}
		}
	}

	// Use the most recent entry as end
	endEntry = &m.dischargeHistory[len(m.dischargeHistory)-1]

	if startEntry == nil || endEntry == nil || startEntry.Timestamp.Equal(endEntry.Timestamp) {
		return 0, fmt.Errorf("insufficient data in time window")
	}

	// Calculate percentage drop per hour
	timeDiff := endEntry.Timestamp.Sub(startEntry.Timestamp).Hours()
	if timeDiff <= 0 {
		return 0, fmt.Errorf("invalid time difference")
	}

	percentDrop := startEntry.Percent - endEntry.Percent
	ratePerHour := percentDrop / float32(timeDiff)

	// Ensure rate is positive (discharge rate)
	if ratePerHour < 0 {
		ratePerHour = 0
	}

	return ratePerHour, nil
}

// UpdateDischargeStatistics calculates and updates discharge rate statistics
func (m *BatteryMonitor) UpdateDischargeStatistics() {
	// Calculate rates for different time windows
	shortTermRate, err := m.CalculateDischargeRate(30 * time.Minute)
	if err == nil {
		m.dischargeStats.ShortTermRate = shortTermRate
	}

	mediumTermRate, err := m.CalculateDischargeRate(6 * time.Hour)
	if err == nil {
		m.dischargeStats.MediumTermRate = mediumTermRate
	}

	longTermRate, err := m.CalculateDischargeRate(24 * time.Hour)
	if err == nil {
		m.dischargeStats.LongTermRate = longTermRate
	}

	// Calculate weighted average
	weights := struct {
		short, medium, long float32
		total               float32
	}{0, 0, 0, 0}

	if m.dischargeStats.ShortTermRate > 0 {
		weights.short = 0.5
		weights.total += weights.short
	}
	if m.dischargeStats.MediumTermRate > 0 {
		weights.medium = 0.3
		weights.total += weights.medium
	}
	if m.dischargeStats.LongTermRate > 0 {
		weights.long = 0.2
		weights.total += weights.long
	}

	if weights.total > 0 {
		m.dischargeStats.AverageRate = (m.dischargeStats.ShortTermRate*weights.short +
			m.dischargeStats.MediumTermRate*weights.medium +
			m.dischargeStats.LongTermRate*weights.long) / weights.total
	}

	m.dischargeStats.DataPoints = len(m.dischargeHistory)
	m.dischargeStats.LastUpdated = time.Now()
}

// GetDepletionEstimate calculates time till battery depletion
func (m *BatteryMonitor) GetDepletionEstimate() *DepletionEstimate {
	if m.lastValidStatus == nil || m.lastValidStatus.Percent <= 0 {
		return nil
	}

	// Update discharge statistics first
	m.UpdateDischargeStatistics()

	// Determine which rate to use
	var dischargeRate float32
	var method string

	// Prefer short-term rate if available and significant
	if m.dischargeStats.ShortTermRate > 0.1 && m.dischargeStats.DataPoints >= 15 {
		dischargeRate = m.dischargeStats.ShortTermRate
		method = "short_term"
	} else if m.dischargeStats.AverageRate > 0 {
		dischargeRate = m.dischargeStats.AverageRate
		method = "averaged"
	} else if m.historicalAverages[m.lastValidStatus.Type] > 0 {
		dischargeRate = m.historicalAverages[m.lastValidStatus.Type]
		method = "historical"
	} else {
		// No data available
		return &DepletionEstimate{
			EstimatedHours:     -1,
			EstimatedDepletion: time.Time{},
			Confidence:         0,
			Method:             "none",
			WarningLevel:       "normal",
		}
	}

	// Calculate remaining hours
	currentPercent := m.lastValidStatus.Percent
	remainingHours := currentPercent / dischargeRate

	// Cap at reasonable maximum
	if remainingHours > 720 { // 30 days
		remainingHours = 720
	}

	// Calculate depletion time
	depletionTime := time.Now().Add(time.Duration(remainingHours) * time.Hour)

	// Determine warning level
	warningLevel := "normal"
	if remainingHours < 6 {
		warningLevel = "critical"
	} else if remainingHours < m.config.DepletionWarningHours {
		warningLevel = "low"
	}

	// Calculate confidence (will be implemented in next task)
	confidence := m.calculateDepletionConfidence(method)

	return &DepletionEstimate{
		EstimatedHours:     remainingHours,
		EstimatedDepletion: depletionTime,
		Confidence:         confidence,
		Method:             method,
		WarningLevel:       warningLevel,
	}
}

// calculateDepletionConfidence calculates confidence score for depletion estimate
func (m *BatteryMonitor) calculateDepletionConfidence(method string) float32 {
	var confidence float32 = 0

	// Data availability score (0-40%)
	if len(m.dischargeHistory) > 0 {
		dataAge := time.Since(m.dischargeHistory[0].Timestamp).Hours()

		if dataAge >= 24 {
			confidence += 40
		} else if dataAge >= 6 {
			confidence += 30
		} else if dataAge >= 0.5 {
			confidence += 20
		} else {
			confidence += 10
		}
	}

	// Rate stability score (0-30%)
	if m.dischargeStats.ShortTermRate > 0 && m.dischargeStats.MediumTermRate > 0 {
		// Calculate variance between rates
		rateDiff := math.Abs(float64(m.dischargeStats.ShortTermRate - m.dischargeStats.MediumTermRate))
		avgRate := float64(m.dischargeStats.ShortTermRate+m.dischargeStats.MediumTermRate) / 2

		if avgRate > 0 {
			variance := rateDiff / avgRate
			if variance < 0.1 { // Less than 10% variance
				confidence += 30
			} else if variance < 0.3 { // Less than 30% variance
				confidence += 20
			} else if variance < 0.5 { // Less than 50% variance
				confidence += 10
			}
		}
	}

	// Battery type certainty (0-30%)
	if m.currentType != nil {
		if m.config.PresetBatteryType != "" || m.config.CustomBatteryType != nil {
			// Configured type
			confidence += 30
		} else if m.voltageRangeReadings >= 20 {
			// Auto-detected with good data
			confidence += 20
		} else {
			// Auto-detected with limited data
			confidence += 10
		}
	}

	// Method penalty
	if method == "historical" {
		confidence = confidence * 0.7 // Reduce confidence for historical data
	}

	// Ensure confidence is within bounds
	if confidence > 100 {
		confidence = 100
	}

	return confidence
}


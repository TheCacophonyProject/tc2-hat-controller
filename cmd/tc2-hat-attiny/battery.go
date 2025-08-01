package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

	// Discharge rate calculation
	minPercentChangeForRate  = 0.2  // Minimum percentage change to calculate discharge rate
	maxRateChangePercent     = 0.2  // Maximum 20% change in discharge rate between updates
	displayHysteresisPercent = 0.05 // 5% change required to update display

	// CSV Bootstrap configuration
	csvBootstrapEnabled        = true        // Enable CSV bootstrap by default
	csvBootstrapTimeWindow     = 48          // Hours of CSV data to consider for bootstrap
	csvBootstrapMinEntries     = 3           // Minimum entries required to trigger bootstrap
	csvBootstrapMaxEntries     = 200         // Maximum entries to load from CSV
	csvBootstrapMinInterval    = 5           // Minimum minutes between CSV entries
	csvBootstrapMaxEntryAge    = 4           // Hours - consider state stale if most recent entry is older
)

// BatteryStatus represents the complete battery state
type BatteryStatus struct {
	Voltage              float32            `json:"voltage"`
	Percent              float32            `json:"percent"`
	Chemistry            string             `json:"chemistry"`
	CellCount            int                `json:"cell_count"`
	Rail                 string             `json:"rail"`
	LastUpdated          time.Time          `json:"last_updated"`
	Error                string             `json:"error,omitempty"`
	DepletionEstimate    *DepletionEstimate `json:"depletion_estimate,omitempty"`
	DischargeRatePerHour float32            `json:"discharge_rate_per_hour"`
	ChargingDetected     bool               `json:"charging_detected"`
}

// BatteryMonitor manages stateful battery monitoring
type BatteryMonitor struct {
	config      *goconfig.Battery
	currentPack *goconfig.BatteryPack
	
	// Voltage tracking
	voltageHistory       []timestampedVoltage
	observedMinVoltage   float32
	observedMaxVoltage   float32
	voltageRangeReadings int

	// Rail tracking for dynamic detection
	hvRailHistory             []timestampedVoltage
	lvRailHistory             []timestampedVoltage
	activeRail                string
	railDeterminationReadings int

	// Status tracking
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

	// Smoothing and filtering
	smoothedDischargeRate  float32
	dischargeRateAlpha     float32
	dischargeRateWindow    []float32
	lastDisplayedHours     float32
	lastDisplayedMethod    string
	lastPercentForRateCalc float32
	lastTimeForRateCalc    time.Time
}

// timestampedVoltage holds voltage with timestamp for stability calculation
type timestampedVoltage struct {
	voltage   float32
	timestamp time.Time
}

// PersistentState represents the saved battery state
type PersistentState struct {
	DetectedChemistry    string    `json:"detected_chemistry,omitempty"`
	DetectedCellCount    int       `json:"detected_cell_count"`
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

	// Smoothing state
	SmoothedDischargeRate float32   `json:"smoothed_discharge_rate"`
	DischargeRateWindow   []float32 `json:"discharge_rate_window"`
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
		dischargeRateAlpha:  0.1,                    // 10% new value, 90% old value
		dischargeRateWindow: make([]float32, 0, 20), // Keep last 20 rate calculations
		lastDisplayedHours:  -1,
		lastDisplayedMethod: "",
	}

	// Load persistent state if available
	if err := monitor.loadPersistentState(); err != nil {
		log.Printf("Could not load persistent battery state: %v", err)
	}

	// Handle initial battery detection/configuration
	if monitor.config.IsManuallyConfigured() {
		// For manual configuration, we still need a voltage reading to determine cell count
		// This will be handled in ensureBatteryPack when we get the first voltage reading
		log.Printf("Manual battery chemistry configured: %s", monitor.config.Chemistry)
	} else if monitor.currentPack == nil {
		// Try CSV-based detection first for faster startup
		if err := monitor.tryCSVBasedDetection(); err != nil {
			log.Printf("CSV-based detection not available: %v", err)
			log.Printf("Will perform auto-detection after collecting voltage readings")
		} else {
			// Save the CSV-detected state
			monitor.savePersistentState()
		}
	}

	return monitor, nil
}

// ProcessReading processes new voltage readings and returns battery status
func (m *BatteryMonitor) ProcessReading(hvBat, lvBat, rtcBat float32) *BatteryStatus {
	m.rtcVoltage = rtcBat

	// Select voltage source
	voltage, rail := m.determineActiveRail(hvBat, lvBat)

	// Check minimum detection threshold
	if voltage < m.config.MinimumVoltageDetection {
		status := &BatteryStatus{
			Voltage:     voltage,
			Percent:     -1,
			Chemistry:   "unknown",
			CellCount:   0,
			Rail:        rail,
			Error:       fmt.Sprintf("voltage %.2fV below detection threshold", voltage),
			LastUpdated: time.Now(),
		}

		// Use last valid status if available
		if m.lastValidStatus != nil {
			status.Percent = m.lastValidStatus.Percent
			status.Chemistry = m.lastValidStatus.Chemistry
			status.CellCount = m.lastValidStatus.CellCount
		}

		return status
	}

	// Update voltage history
	m.addToHistory(voltage)

	// Handle battery pack detection/validation
	if err := m.ensureBatteryPack(voltage); err != nil {
		status := &BatteryStatus{
			Voltage:     voltage,
			Percent:     -1,
			Chemistry:   "unknown",
			CellCount:   0,
			Rail:        rail,
			Error:       err.Error(),
			LastUpdated: time.Now(),
		}

		// Use last valid percentage if available
		if m.lastValidStatus != nil {
			status.Percent = m.lastValidStatus.Percent
			status.Chemistry = m.lastValidStatus.Chemistry
			status.CellCount = m.lastValidStatus.CellCount
		}

		return status
	}

	// Calculate percentage
	percent, err := m.calculatePercent(voltage)
	if err != nil {
		return &BatteryStatus{
			Voltage:     voltage,
			Percent:     -1,
			Chemistry:   m.currentPack.Type.Chemistry,
			CellCount:   m.currentPack.CellCount,
			Rail:        rail,
			Error:       err.Error(),
			LastUpdated: time.Now(),
		}
	}

	// Create successful status
	status := &BatteryStatus{
		Voltage:     voltage,
		Percent:     percent,
		Chemistry:   m.currentPack.Type.Chemistry,
		CellCount:   m.currentPack.CellCount,
		Rail:        rail,
		LastUpdated: time.Now(),
	}

	// Calculate discharge rate BEFORE updating history (in case history gets cleared)
	if m.config.EnableDepletionEstimate {
		// Set discharge rate from existing history
		if rate := m.calculateBestDischargeRate(); rate > 0 {
			status.DischargeRatePerHour = rate
			log.Printf("Using discharge rate: %.3f%%/hour (current: %.1f%%, voltage: %.2fV)", rate, percent, voltage)
		} else {
			log.Printf("No valid discharge rate available (history entries: %d)", len(m.dischargeHistory))
		}
		
		// Now update discharge history (this might clear history due to charging detection)
		m.UpdateDischargeHistory(status)

		// Get depletion estimate if we still have data after history update
		if len(m.dischargeHistory) > 1 {
			status.DepletionEstimate = m.GetDepletionEstimate()
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

func (m *BatteryMonitor) ensureBatteryPack(voltage float32) error {
	// Store previous chemistry/cell count for comparison
	previousChemistry := ""
	previousCellCount := 0
	if m.currentPack != nil {
		previousChemistry = m.currentPack.Type.Chemistry
		previousCellCount = m.currentPack.CellCount
	}

	// Check if battery chemistry is manually configured
	if m.config.IsManuallyConfigured() {
		// Use manually configured chemistry
		pack, err := m.config.GetBatteryPack(voltage)
		if err != nil {
			// Check if the error is due to no battery chemistry being specified
			if strings.Contains(err.Error(), "no battery chemistry specified") {
				log.Printf("Manual configuration enabled but no battery chemistry specified - disabling manual configuration and falling back to auto-detection")
				m.config.ClearManualConfiguration()
				// Fall through to auto-detection logic below
			} else {
				return fmt.Errorf("manually configured battery chemistry not found: %v", err)
			}
		} else {
			if m.currentPack == nil || 
			   m.currentPack.Type.Chemistry != pack.Type.Chemistry || 
			   m.currentPack.CellCount != pack.CellCount {
				m.currentPack = pack
				log.Printf("Using manually configured battery: %s chemistry, %d cells",
					m.currentPack.Type.Chemistry, m.currentPack.CellCount)

				if previousChemistry != "" && 
				   (previousChemistry != m.currentPack.Type.Chemistry || previousCellCount != m.currentPack.CellCount) {
					log.Printf("Manual battery chemistry changed from %s %dcells to %s %dcells - preserving discharge history", 
						previousChemistry, previousCellCount, m.currentPack.Type.Chemistry, m.currentPack.CellCount)
				}
				m.savePersistentState()
			}
			return nil
		}
	} 
	

	// Update voltage range tracking
	m.updateVoltageRange(voltage)

	// Check if we need to re-detect due to significant voltage change
	if m.currentPack != nil {
		minVoltage := m.currentPack.GetScaledMinVoltage()
		maxVoltage := m.currentPack.GetScaledMaxVoltage()
		
		// Check if voltage is outside the expected range (with 1V tolerance)
		if voltage < minVoltage-1.0 || voltage > maxVoltage+1.0 {
			log.Printf("Voltage %.2fV is outside expected range [%.2f-%.2f] for %s %d cells. Re-detecting...",
				voltage, minVoltage-1.0, maxVoltage+1.0, m.currentPack.Type.Chemistry, m.currentPack.CellCount)
			
			// Attempt immediate re-detection with current voltage
			chemistry, cellCount, err := m.detectChemistry(voltage)
			if err == nil {
				newPack := &goconfig.BatteryPack{
					Type:      chemistry,
					CellCount: cellCount,
				}
				
				// Verify the new detection makes sense for this voltage
				newMin := newPack.GetScaledMinVoltage()
				newMax := newPack.GetScaledMaxVoltage()
				if voltage >= newMin-0.5 && voltage <= newMax+0.5 {
					log.Printf("Re-detected battery: %s chemistry, %d cells (voltage %.2fV fits range [%.2f-%.2f])",
						newPack.Type.Chemistry, newPack.CellCount, voltage, newMin, newMax)
					m.currentPack = newPack
					m.clearDischargeHistory("battery type changed")
					m.savePersistentState()
					return nil
				}
			}
		}
		return nil
	}

	// No current pack - need initial detection
	// Attempt to detect chemistry and cells immediately
	err := m.detectChemistryAndCells(voltage)
	
	// If detection was successful, log any changes from the previous state
	if err == nil && m.currentPack != nil {
		if previousChemistry != "" && (previousChemistry != m.currentPack.Type.Chemistry || previousCellCount != m.currentPack.CellCount) {
			log.Printf("Auto-detected battery chemistry updated from %s %dcells to %s %dcells - preserving discharge history",
				previousChemistry, previousCellCount, m.currentPack.Type.Chemistry, m.currentPack.CellCount)
		}
	}

	return err
}

func (m *BatteryMonitor) clearDischargeHistory(reason string) {
	log.Printf("Clearing discharge history: %s", reason)
	m.dischargeHistory = make([]DischargeRateHistory, 0)
	m.dischargeStats = DischargeStatistics{}
	m.smoothedDischargeRate = 0
	m.dischargeRateWindow = make([]float32, 0, 20)
	m.lastDisplayedHours = -1
	m.lastDisplayedMethod = ""
	m.lastPercentForRateCalc = 0
	m.lastTimeForRateCalc = time.Time{}
}

// detectChemistry determines the most likely battery chemistry based on voltage characteristics
func (m *BatteryMonitor) detectChemistry(voltage float32) (*goconfig.BatteryType, int, error) {
	// Use the new AutoDetectBatteryPack function from go-config
	pack, err := goconfig.AutoDetectBatteryPack(voltage)
	if err != nil {
		return nil, 0, err
	}
	
	log.Printf("Detected chemistry: %s, %d cells (%.1f-%.1fV range) for voltage %.2fV", 
		pack.Type.Chemistry, pack.CellCount, 
		pack.GetScaledMinVoltage(), pack.GetScaledMaxVoltage(),
		voltage)
	
	return pack.Type, pack.CellCount, nil
}


// detectChemistryAndCells attempts to detect battery chemistry and cell count from voltage
func (m *BatteryMonitor) detectChemistryAndCells(voltage float32) error {
	// Safety check: never override manual configuration
	if m.config.IsManuallyConfigured() {
		return fmt.Errorf("auto-detection called on manually configured battery - this should not happen")
	}

	// Validate voltage is reasonable
	if voltage <= 0 {
		return fmt.Errorf("invalid voltage for detection: %.2fV", voltage)
	}
	
	if voltage > 60.0 {
		return fmt.Errorf("voltage %.2fV exceeds safety limit for auto-detection", voltage)
	}


	// Step 1: Detect chemistry and cell count together
	chemistry, cellCount, err := m.detectChemistry(voltage)
	if err != nil {
		// Provide helpful error with suggestions
		var suggestions []string
		for chemName, chem := range goconfig.ChemistryProfiles {
			nominalVoltage := (chem.MinVoltage + chem.MaxVoltage) / 2
			closestCells := int(voltage/nominalVoltage + 0.5)
			if closestCells < 1 {
				closestCells = 1
			} else if closestCells > 24 {
				closestCells = 24
			}
			scaledMin := chem.MinVoltage * float32(closestCells)
			scaledMax := chem.MaxVoltage * float32(closestCells)
			suggestions = append(suggestions, fmt.Sprintf("%s %dcells: %.1f-%.1fV", 
				chemName, closestCells, scaledMin, scaledMax))
		}
		
		return fmt.Errorf("failed to detect chemistry for %.2fV. Possible matches: %s", 
			voltage, strings.Join(suggestions, ", "))
	}

	// Create the detected battery pack
	m.currentPack = &goconfig.BatteryPack{
		Type:      chemistry,
		CellCount: cellCount,
	}
	
	log.Printf("Auto-detected battery: %s chemistry, %d cells (%.1f-%.1fV) based on voltage %.2fV",
		m.currentPack.Type.Chemistry, m.currentPack.CellCount,
		m.currentPack.GetScaledMinVoltage(), m.currentPack.GetScaledMaxVoltage(),
		voltage)
	
	m.savePersistentState()
	return nil
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
	m.currentPack = nil
	m.observedMinVoltage = 999.0
	m.observedMaxVoltage = 0.0
	m.voltageRangeReadings = 0
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
	if m.currentPack == nil {
		return -1, fmt.Errorf("no battery pack detected")
	}

	return m.currentPack.VoltageToPercent(voltage)
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

	// Report on chemistry/cell count change
	if m.lastValidStatus != nil && 
	   (m.lastValidStatus.Chemistry != status.Chemistry || m.lastValidStatus.CellCount != status.CellCount) {
		return true
	}

	return false
}

func (m *BatteryMonitor) GetRTCVoltage() float32 {
	return m.rtcVoltage
}

// tryCSVBasedDetection attempts to detect battery chemistry from CSV history
func (m *BatteryMonitor) tryCSVBasedDetection() error {
	// Read recent entries from CSV to get voltage range
	csvFilePath := "/var/log/battery-readings.csv"
	file, err := os.Open(csvFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no CSV file available")
		}
		return fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true

	var voltages []float32
	cutoffTime := time.Now().Add(-4 * time.Hour) // Look at last 4 hours only

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		// Skip header
		if len(record) < 8 || strings.Contains(record[0], "timestamp") {
			continue
		}

		timestamp, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(record[0]))
		if err != nil || timestamp.Before(cutoffTime) {
			continue
		}

		// Column 1 is HV battery voltage
		voltage, err := strconv.ParseFloat(strings.TrimSpace(record[1]), 32)
		if err != nil || voltage <= 0 {
			continue
		}

		voltages = append(voltages, float32(voltage))
	}

	if len(voltages) < 5 {
		return fmt.Errorf("insufficient voltage readings in CSV (%d found)", len(voltages))
	}

	// Calculate average voltage for detection
	var sum float32
	for _, v := range voltages {
		sum += v
	}
	avgVoltage := sum / float32(len(voltages))

	// Detect chemistry and cells based on average voltage
	chemistry, cellCount, err := m.detectChemistry(avgVoltage)
	if err != nil {
		return fmt.Errorf("failed to detect chemistry from CSV average voltage %.2fV: %w", avgVoltage, err)
	}

	// Create the detected battery pack
	m.currentPack = &goconfig.BatteryPack{
		Type:      chemistry,
		CellCount: cellCount,
	}

	// Update voltage range from CSV data
	for _, v := range voltages {
		m.updateVoltageRange(v)
	}

	log.Printf("CSV-based auto-detection: %s chemistry, %d cells based on %d readings (avg %.2fV)",
		m.currentPack.Type.Chemistry, m.currentPack.CellCount, len(voltages), avgVoltage)

	return nil
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

	// Restore smoothing state
	m.smoothedDischargeRate = state.SmoothedDischargeRate
	if state.DischargeRateWindow != nil {
		m.dischargeRateWindow = state.DischargeRateWindow
	}

	// Only use saved state if no manual configuration and state is recent
	if !m.config.IsManuallyConfigured() && state.DetectedChemistry != "" && state.DetectedCellCount > 0 &&
		time.Since(state.LastUpdated) < 24*time.Hour {
		// Check if the chemistry profile still exists
		if chemistryProfile, exists := goconfig.ChemistryProfiles[state.DetectedChemistry]; exists {
			m.currentPack = &goconfig.BatteryPack{
				Type:      &chemistryProfile,
				CellCount: state.DetectedCellCount,
			}
			log.Printf("Restored battery pack from state: %s chemistry, %d cells",
				m.currentPack.Type.Chemistry, m.currentPack.CellCount)
		}
	}

	// Check if we need to load CSV data due to insufficient discharge history
	// Only attempt CSV bootstrap if we have a detected battery pack
	if m.currentPack != nil {
		shouldLoadCSV := false
		
		// Load CSV if discharge history is empty or very sparse
		if len(m.dischargeHistory) == 0 {
			log.Printf("No discharge history found in state, loading from CSV")
			shouldLoadCSV = true
		} else if len(m.dischargeHistory) < csvBootstrapMinEntries {
			log.Printf("Insufficient discharge history (%d entries), loading from CSV", len(m.dischargeHistory))
			shouldLoadCSV = true
		} else {
			// Check if the most recent entry is too old
			mostRecent := m.dischargeHistory[len(m.dischargeHistory)-1]
			maxAge := time.Duration(csvBootstrapMaxEntryAge) * time.Hour
			if time.Since(mostRecent.Timestamp) > maxAge {
				log.Printf("Most recent discharge history entry is %v old, loading from CSV",
					time.Since(mostRecent.Timestamp))
				shouldLoadCSV = true
			}
		}
		
		if shouldLoadCSV {
			if err := m.bootstrapFromCSV(); err != nil {
				log.Printf("CSV loading failed: %v", err)
			}
		}
	}

	return nil
}

func (m *BatteryMonitor) savePersistentState() {
	state := PersistentState{
		ObservedMinVoltage:    m.observedMinVoltage,
		ObservedMaxVoltage:    m.observedMaxVoltage,
		VoltageRangeReadings:  m.voltageRangeReadings,
		ActiveRail:            m.activeRail,
		LastUpdated:           time.Now(),
		DischargeHistory:      m.dischargeHistory,
		DischargeStats:        m.dischargeStats,
		LastChargeEvent:       m.lastChargeEvent,
		HistoricalAverages:    m.historicalAverages,
		SmoothedDischargeRate: m.smoothedDischargeRate,
		DischargeRateWindow:   m.dischargeRateWindow,
	}

	if m.currentPack != nil {
		state.DetectedChemistry = m.currentPack.Type.Chemistry
		state.DetectedCellCount = m.currentPack.CellCount
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

// bootstrapFromCSV loads recent discharge history from the CSV log file
// when the state file is missing or has insufficient data
func (m *BatteryMonitor) bootstrapFromCSV() error {
	if !csvBootstrapEnabled {
		return fmt.Errorf("CSV bootstrap is disabled")
	}

	// CRITICAL: We cannot bootstrap without knowing what the current battery is.
	// If the pack hasn't been detected yet, we must abort.
	if m.currentPack == nil {
		log.Printf("Cannot bootstrap from CSV: no current battery pack has been detected yet.")
		return fmt.Errorf("current pack not set, cannot interpret historical voltages")
	}

	csvFilePath := "/var/log/battery-readings.csv"
	file, err := os.Open(csvFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No file to bootstrap from, not an error.
		}
		return fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true

	var entries []DischargeRateHistory
	cutoffTime := time.Now().Add(-time.Duration(csvBootstrapTimeWindow) * time.Hour)

	log.Printf("Bootstrapping with current pack: %s %d cells. All historical data will be interpreted using this profile.", m.currentPack.Type.Chemistry, m.currentPack.CellCount)
	
	minValidVoltage := m.currentPack.GetScaledMinVoltage() - 1.0 // With 1.0V tolerance
	maxValidVoltage := m.currentPack.GetScaledMaxVoltage() + 1.0 // With 1.0V tolerance

	var rawEntries, skippedEntries, errorEntries int

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			errorEntries++
			continue
		}

		rawEntries++

		// Basic format and header check
		if len(record) < 8 || strings.Contains(record[0], "timestamp") {
			skippedEntries++
			continue
		}

		timestamp, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(record[0]))
		if err != nil || timestamp.Before(cutoffTime) {
			continue
		}
		
		// We only need the voltage from the historical record.
		voltage, err := strconv.ParseFloat(strings.TrimSpace(record[1]), 32)
		if err != nil {
			continue
		}
		
		// --- START: CORE LOGIC ---

		// 1. Validate that the historical voltage is plausible for the CURRENT pack.
		if float32(voltage) < minValidVoltage || float32(voltage) > maxValidVoltage {
			skippedEntries++
			continue // This voltage doesn't belong to the current battery type.
		}
		
		// 2. Recalculate the percentage using the CURRENT pack's profile.
		// This is the most important step for ensuring data consistency.
		recalculatedPercent, err := m.currentPack.VoltageToPercent(float32(voltage))
		if err != nil {
			skippedEntries++ // Voltage is in the general range but invalid for the curve.
			continue
		}

		// --- END: CORE LOGIC ---

		entries = append(entries, DischargeRateHistory{
			Timestamp: timestamp,
			Voltage:   float32(voltage),
			Percent:   recalculatedPercent, // Always use the newly calculated, consistent percentage.
		})
	}
	
	if len(entries) < csvBootstrapMinEntries {
		return fmt.Errorf("insufficient valid entries found in CSV file (%d found, %d required)", len(entries), csvBootstrapMinEntries)
	}

	// Filter out charging events to get a clean discharge history.
	var dischargeOnlyEntries []DischargeRateHistory
	for _, entry := range entries {
		// A jump in voltage or percentage indicates a charge/swap. Reset the history.
		if len(dischargeOnlyEntries) > 0 {
			lastEntry := dischargeOnlyEntries[len(dischargeOnlyEntries)-1]
			if entry.Voltage > lastEntry.Voltage+0.5 || entry.Percent > lastEntry.Percent+5.0 {
				log.Printf("CSV bootstrap: charging event or data anomaly detected at %v (%.2fV->%.2fV, %.1f%%->%.1f%%). Discarding prior history.",
					entry.Timestamp.Format("15:04"), lastEntry.Voltage, entry.Voltage, lastEntry.Percent, entry.Percent)
				dischargeOnlyEntries = make([]DischargeRateHistory, 0)
			}
		}
		dischargeOnlyEntries = append(dischargeOnlyEntries, entry)
	}
	
	// Validate overall discharge rate from CSV data
	if len(dischargeOnlyEntries) > 1 {
		firstEntry := dischargeOnlyEntries[0]
		lastEntry := dischargeOnlyEntries[len(dischargeOnlyEntries)-1]
		totalHours := lastEntry.Timestamp.Sub(firstEntry.Timestamp).Hours()
		if totalHours > 0.5 {
			percentDrop := firstEntry.Percent - lastEntry.Percent
			overallRate := percentDrop / float32(totalHours)
			log.Printf("CSV bootstrap validation: %.1f%% drop over %.1f hours = %.3f%%/hour overall rate",
				percentDrop, totalHours, overallRate)
			
			// If overall rate is unrealistic, something is wrong with the data
			if overallRate > 3.0 {
				log.Printf("WARNING: CSV data shows unrealistic discharge rate %.3f%%/hour - may indicate configuration mismatch", overallRate)
			}
		}
	}
	
	if len(dischargeOnlyEntries) < csvBootstrapMinEntries {
		return fmt.Errorf("insufficient discharge-only entries after filtering (%d)", len(dischargeOnlyEntries))
	}
	
	// Final filtering by time interval.
	var filteredEntries []DischargeRateHistory
	lastTime := time.Time{}
	minInterval := time.Duration(csvBootstrapMinInterval) * time.Minute
	for _, entry := range dischargeOnlyEntries {
		if lastTime.IsZero() || entry.Timestamp.Sub(lastTime) >= minInterval {
			filteredEntries = append(filteredEntries, entry)
			lastTime = entry.Timestamp
		}
	}

	if len(filteredEntries) < csvBootstrapMinEntries {
		return fmt.Errorf("not enough filtered entries after time sampling (%d)", len(filteredEntries))
	}
	
	if len(filteredEntries) > csvBootstrapMaxEntries {
		filteredEntries = filteredEntries[len(filteredEntries)-csvBootstrapMaxEntries:]
	}

	m.dischargeHistory = filteredEntries

	finalTimeSpan := time.Duration(0)
	if len(filteredEntries) > 1 {
		finalTimeSpan = filteredEntries[len(filteredEntries)-1].Timestamp.Sub(filteredEntries[0].Timestamp)
	}

	log.Printf("CSV bootstrap completed successfully:")
	log.Printf("  - Processed %d raw CSV entries", rawEntries)
	log.Printf("  - Extracted %d valid discharge entries", len(dischargeOnlyEntries))
	log.Printf("  - Finalized with %d entries after time sampling", len(filteredEntries))
	log.Printf("  - Time span of data: %v", finalTimeSpan)

	return nil
}

func monitorVoltageLoop(a *attiny, config *goconfig.Config) {
	stateDir := "/var/lib/tc2-hat-controller"
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
	configReloadCounter := 0

	// Create a separate ticker for signal checking
	signalTicker := time.NewTicker(1 * time.Second)
	defer signalTicker.Stop()

	// Main battery reading ticker
	batteryTicker := time.NewTicker(2 * time.Minute)
	defer batteryTicker.Stop()
	for {
		select {
		case <-signalTicker.C:
			// Check for config signal every 10 seconds
			if checkBatteryConfigSignal(config, batteryMonitor) {
				// Config was reloaded, perform immediate reading
				status, hvBat, lvBat, rtcBat, err := performBatteryReading(a, batteryMonitor)
				if err != nil {
					log.Printf("Error during immediate battery reading after config reload: %v", err)
				} else {
					log.Printf("Immediate reading after config reload: HV=%.2f, LV=%.2f, RTC=%.2f - %s %dcells %.1f%% on %s rail",
						hvBat, lvBat, rtcBat, status.Chemistry, status.CellCount, status.Percent, status.Rail)

					if batteryMonitor.ShouldReportEvent(status) {
						reportBatteryEvent(status, rtcBat)
						if err := sendBatterySignal(float64(status.Voltage), float64(status.Percent)); err != nil {
							log.Error("Error sending battery signal:", err)
						}
					}
				}
			}

		case <-batteryTicker.C:
			// Reload config every 30 iterations (every hour) as backup
			if configReloadCounter >= 30 {
				log.Printf("Periodic battery configuration reload...")
				if err := config.Reload(); err != nil {
					log.Printf("Failed to reload config: %v", err)
				} else {
					// Update battery monitor config
					if err := config.Unmarshal(goconfig.BatteryKey, batteryMonitor.config); err != nil {
						log.Printf("Failed to unmarshal battery config: %v", err)
					} else {
						log.Printf("Battery configuration reloaded (periodic)")
						// If switching from auto to manual, reset the current pack to force re-detection
						if batteryMonitor.config.IsManuallyConfigured() && batteryMonitor.currentPack != nil {
							if batteryMonitor.currentPack.Type.Chemistry != batteryMonitor.config.Chemistry {
								batteryMonitor.currentPack = nil
							}
						}
					}
				}
				configReloadCounter = 0
			}
			configReloadCounter++

			// Perform regular battery reading
			status, hvBat, lvBat, rtcBat, err := performBatteryReading(a, batteryMonitor)
			if err != nil {
				log.Error("Error during battery reading:", err)
				time.Sleep(2 * time.Minute)
				continue
			}

			// Log to console periodically
			if logCounter >= 5 {
				if status.Error != "" {
					log.Printf("Battery reading: HV=%.2f, LV=%.2f, RTC=%.2f - Error: %s",
						hvBat, lvBat, rtcBat, status.Error)
				} else {
					log.Printf("Battery reading: HV=%.2f, LV=%.2f, RTC=%.2f - %s %dcells %.1f%% on %s rail",
						hvBat, lvBat, rtcBat, status.Chemistry, status.CellCount, status.Percent, status.Rail)
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
				log.Warnf("Low battery warning: %.1f%% (%s %dcells)", status.Percent, status.Chemistry, status.CellCount)
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
		}
	}
}

// checkBatteryConfigSignal checks for battery config change signal and reloads config if needed
func checkBatteryConfigSignal(config *goconfig.Config, batteryMonitor *BatteryMonitor) bool {
	signalFile := "/tmp/battery-config-changed"

	// Check if signal file exists
	if _, err := os.Stat(signalFile); os.IsNotExist(err) {
		return false
	}

	// Signal file exists, reload config
	log.Printf("Battery config change signal detected, reloading configuration...")

	// Remove signal file
	if err := os.Remove(signalFile); err != nil {
		log.Printf("Failed to remove signal file: %v", err)
		// Continue anyway
	}

	// Reload config
	if err := config.Reload(); err != nil {
		log.Printf("Failed to reload config: %v", err)
		return false
	}

	// Update battery monitor config
	if err := config.Unmarshal(goconfig.BatteryKey, batteryMonitor.config); err != nil {
		log.Printf("Failed to unmarshal battery config: %v", err)
		return false
	}

	// If switching from auto to manual or chemistry changed, reset current pack
	if batteryMonitor.config.IsManuallyConfigured() {
		if batteryMonitor.currentPack == nil || batteryMonitor.currentPack.Type.Chemistry != batteryMonitor.config.Chemistry {
			batteryMonitor.currentPack = nil
			log.Printf("Manual chemistry %s configured - will determine cell count on next reading", batteryMonitor.config.Chemistry)
		}
	} else {
		// Switching to auto-detection, clear any manual pack
		if batteryMonitor.currentPack != nil && batteryMonitor.config.Chemistry == "" {
			batteryMonitor.currentPack = nil
			batteryMonitor.resetDetection()
			log.Printf("Switched to auto-detection mode")
		}
	}

	log.Printf("Battery configuration reloaded immediately due to signal")
	return true
}

// performBatteryReading performs a single battery reading cycle
func performBatteryReading(a *attiny, batteryMonitor *BatteryMonitor) (*BatteryStatus, float32, float32, float32, error) {
	// Read voltage values from ATtiny
	hvBat, err := a.readHVBattery()
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("error reading HV battery: %w", err)
	}

	lvBat, err := a.readLVBattery()
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("error reading LV battery: %w", err)
	}

	rtcBat, err := a.readRTCBattery()
	if err != nil {
		log.Error("Error reading RTC battery:", err)
		rtcBat = 0 // Continue without RTC voltage
	}

	// Process readings with battery monitor
	status := batteryMonitor.ProcessReading(hvBat, lvBat, rtcBat)

	// Log to CSV file
	if err := logBatteryReadingToFile(hvBat, lvBat, rtcBat, status); err != nil {
		log.Error("Error logging battery reading:", err)
	}

	return status, hvBat, lvBat, rtcBat, nil
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

	// Format: timestamp, HV, LV, RTC, chemistry, cells, percent, rail, error, discharge_rate, hours_remaining, confidence
	line := fmt.Sprintf("%s, %.2f, %.2f, %.2f, %s, %d, %.1f, %s, %s, %.2f, %.1f, %.1f",
		time.Now().Format("2006-01-02 15:04:05"),
		hvBat, lvBat, rtcBat,
		status.Chemistry, status.CellCount, status.Percent,
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
			"chemistry":   status.Chemistry,
			"cellCount":   status.CellCount,
			"voltage":     status.Voltage,
			"rail":        status.Rail,
			"rtcVoltage":  fmt.Sprintf("%.2f", rtcVoltage),
		},
	}

	err := eventclient.AddEvent(event)
	if err != nil {
		log.Error("Error sending battery event:", err)
	} else {
		log.Infof("Battery event: chemistry=%s (%dcells), voltage=%.2fV, percent=%d%%",
			status.Chemistry, status.CellCount, status.Voltage, roundedPercent)
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
			"chemistry":      status.Chemistry,
			"cellCount":      status.CellCount,
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

	// Define the signal - no need to request a bus name
	sig := &dbus.Signal{
		Path: dbus.ObjectPath("/org/cacophony/attiny"),
		Name: "org.cacophony.attiny.Battery",
		Body: []interface{}{voltage, percent},
	}

	// Emit the signal
	return conn.Emit(sig.Path, sig.Name, sig.Body...)
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
			log.Infof("Reading %d/%d: HV=%.2fV, LV=%.2fV, RTC=%.2fV - %s %dcells %.1f%% on %s",
				i+1, readings, hvBat, lvBat, rtcBat,
				status.Chemistry, status.CellCount, status.Percent, status.Rail)
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
			log.Infof("Detected Battery: %s chemistry, %d cells", lastStatus.Chemistry, lastStatus.CellCount)

			// Count detection changes
			changes := 0
			prevChemistry := ""
			prevCellCount := 0
			for _, s := range statusHistory {
				if (s.Chemistry != prevChemistry || s.CellCount != prevCellCount) && prevChemistry != "" {
					changes++
				}
				prevChemistry = s.Chemistry
				prevCellCount = s.CellCount
			}
			log.Infof("Detection stability: %d chemistry/cell changes in %d readings", changes, len(statusHistory))
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

	// Check if voltage jumped to near max for battery pack
	if m.currentPack != nil {
		maxThreshold := m.currentPack.GetScaledMaxVoltage() - 0.5
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
			m.dischargeStats = DischargeStatistics{}       // Reset stats
			m.smoothedDischargeRate = 0                    // Reset smoothed rate
			m.dischargeRateWindow = make([]float32, 0, 20) // Clear rate window
			m.lastDisplayedHours = -1                      // Reset displayed hours
			m.lastDisplayedMethod = ""                     // Reset displayed method
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

	var startIndex, endIndex int = -1, len(m.dischargeHistory) - 1
	
	// Find the first entry that is within our time window.
	for i := range m.dischargeHistory {
		if m.dischargeHistory[i].Timestamp.After(cutoffTime) {
			// We need a point *before* the window starts for an accurate baseline.
			if i > 0 {
				startIndex = i - 1
			} else {
				startIndex = i
			}
			break
		}
	}

	if startIndex == -1 {
		// No data in the window, but we might have data older than the window.
		// Use the entire history if it's shorter than the requested duration.
		if now.Sub(m.dischargeHistory[0].Timestamp) < duration {
			startIndex = 0
		} else {
			return 0, fmt.Errorf("no data within the %v time window", duration)
		}
	}
	
	startEntry := m.dischargeHistory[startIndex]
	endEntry := m.dischargeHistory[endIndex]

	if startIndex == endIndex {
		return 0, fmt.Errorf("only one data point in the window")
	}

	timeDiffHours := endEntry.Timestamp.Sub(startEntry.Timestamp).Hours()
	if timeDiffHours < 0.01 { // Less than ~36 seconds
		return 0, fmt.Errorf("time difference too small for accurate calculation: %.2f hours", timeDiffHours)
	}

	percentDrop := startEntry.Percent - endEntry.Percent
	
	// The rate must be positive (discharging). Negative implies charging.
	if percentDrop <= 0 {
		return 0, fmt.Errorf("battery is not discharging (percent change: %.2f)", percentDrop)
	}

	// We have a valid discharge, now check if it's significant enough
	if percentDrop < minPercentChangeForRate {
		return 0, fmt.Errorf("percentage change %.3f%% is below minimum threshold of %.2f%%", percentDrop, minPercentChangeForRate)
	}

	// This is the raw, unsmoothed rate for this specific time window.
	ratePerHour := percentDrop / float32(timeDiffHours)
	
	// Add detailed debugging for discharge rate calculation
	log.Printf("Discharge rate calculation: %.2f%% drop over %.2f hours = %.3f%%/hour (window: %v, start: %.1f%% @ %v, end: %.1f%% @ %v)",
		percentDrop, timeDiffHours, ratePerHour, duration,
		startEntry.Percent, startEntry.Timestamp.Format("15:04:05"),
		endEntry.Percent, endEntry.Timestamp.Format("15:04:05"))
	
	// Sanity check: cap unrealistic discharge rates
	if ratePerHour > 5.0 {
		log.Printf("WARNING: Calculated discharge rate %.3f%%/hour exceeds realistic maximum (5%%/hour) - capping", ratePerHour)
		ratePerHour = 5.0
	}
	
	// Apply smoothing.
	// If smoothedDischargeRate is zero, this is our first valid calculation, so we set it directly.
	if m.smoothedDischargeRate <= 0 {
		m.smoothedDischargeRate = ratePerHour
		log.Printf("First discharge rate calculation: %.3f%%/hour", m.smoothedDischargeRate)
	} else {
		// Use exponential moving average to smooth the new value with the historical average.
		// This prevents wild fluctuations.
		previousRate := m.smoothedDischargeRate
		m.smoothedDischargeRate = (m.dischargeRateAlpha * ratePerHour) + ((1 - m.dischargeRateAlpha) * m.smoothedDischargeRate)
		log.Printf("EWMA discharge rate: raw=%.3f%%/hour, previous=%.3f%%/hour, smoothed=%.3f%%/hour (alpha=%.1f)",
			ratePerHour, previousRate, m.smoothedDischargeRate, m.dischargeRateAlpha)
	}
	
	// Add the new smoothed rate to our rolling window for median calculation.
	m.dischargeRateWindow = append(m.dischargeRateWindow, m.smoothedDischargeRate)
	if len(m.dischargeRateWindow) > 20 {
		m.dischargeRateWindow = m.dischargeRateWindow[1:]
	}

	return m.smoothedDischargeRate, nil
}

// calculateBestDischargeRate tries multiple methods to get the best available discharge rate
func (m *BatteryMonitor) calculateBestDischargeRate() float32 {
	if len(m.dischargeHistory) < 2 {
		return 0
	}
	
	// Try different calculation methods in order of preference
	// Prioritize longer time windows for more stable rates
	timeWindows := []time.Duration{
		6 * time.Hour,     // Longer term (prioritized for stability)
		2 * time.Hour,     // Medium term  
		24 * time.Hour,    // Very long term
		30 * time.Minute,  // Short term (fallback only)
	}
	
	log.Printf("Calculating best discharge rate from %d history entries spanning %v",
		len(m.dischargeHistory),
		m.dischargeHistory[len(m.dischargeHistory)-1].Timestamp.Sub(m.dischargeHistory[0].Timestamp))
	
	for _, window := range timeWindows {
		if rate, err := m.CalculateDischargeRate(window); err == nil && rate > 0 {
			log.Printf("Selected discharge rate %.3f%%/hour from %v time window", rate, window)
			return rate
		} else {
		}
	}
	
	// If none of the time window calculations work, try using existing statistics
	if m.dischargeStats.AverageRate > 0 {
		return m.dischargeStats.AverageRate
	}
	
	if m.dischargeStats.ShortTermRate > 0 {
		return m.dischargeStats.ShortTermRate
	}
	
	// Last resort: use median of rate window if available
	if len(m.dischargeRateWindow) > 0 {
		return calculateMedian(m.dischargeRateWindow)
	}
	
	return 0
}

// UpdateDischargeStatistics calculates and updates discharge rate statistics
func (m *BatteryMonitor) UpdateDischargeStatistics() {
	// Store previous rates for potential fallback
	prevAverageRate := m.dischargeStats.AverageRate
	
	// Calculate rates for different time windows
	shortTermRate, err := m.CalculateDischargeRate(30 * time.Minute)
	if err == nil {
		m.dischargeStats.ShortTermRate = shortTermRate
	} else if m.dischargeStats.ShortTermRate == 0 && prevAverageRate > 0 {
		// If we have no short term rate but had a previous average, use it as a fallback
		m.dischargeStats.ShortTermRate = prevAverageRate
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
	} else if prevAverageRate > 0 {
		// If we can't calculate any new rates but had a previous average, preserve it
		// This prevents losing bootstrap rates due to gaps in recent data
		m.dischargeStats.AverageRate = prevAverageRate
		log.Printf("Preserving previous discharge rate: %.3f%%/hour (no current rates available)", prevAverageRate)
	}

	m.dischargeStats.DataPoints = len(m.dischargeHistory)
	m.dischargeStats.LastUpdated = time.Now()
	
	// Log statistics update for debugging
	if m.dischargeStats.AverageRate > 0 {
		log.Printf("Updated discharge statistics: Short=%.3f, Medium=%.3f, Long=%.3f, Average=%.3f", 
			m.dischargeStats.ShortTermRate, m.dischargeStats.MediumTermRate, 
			m.dischargeStats.LongTermRate, m.dischargeStats.AverageRate)
	}
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

	// Use median of discharge rate window if available
	if len(m.dischargeRateWindow) >= 5 {
		dischargeRate = calculateMedian(m.dischargeRateWindow)
		method = "median_filtered"
	} else if m.dischargeStats.ShortTermRate > 0.01 && m.dischargeStats.DataPoints >= 15 {
		// Lower threshold from 0.1 to 0.01 for very stable batteries
		dischargeRate = m.dischargeStats.ShortTermRate
		method = "short_term"
	} else if m.dischargeStats.AverageRate > 0 {
		dischargeRate = m.dischargeStats.AverageRate
		method = "averaged"
	} else if len(m.dischargeHistory) >= 10 {
		// Try multiple approaches for stable batteries
		
		// First try sampled intervals
		sampledRate, err := m.calculateSampledDischargeRate(10 * time.Minute)
		if err == nil && sampledRate > 0 && sampledRate <= 5.0 {
			dischargeRate = sampledRate
			method = "sampled_intervals"
		} else {
			// Fallback to voltage-based calculation (works regardless of chemistry)
			voltageRate, err := m.calculateVoltageBasedDischargeRate()
			if err == nil && voltageRate > 0 && voltageRate <= 5.0 {
				dischargeRate = voltageRate
				method = "voltage_based"
			}
		}
	}
	
	// Use historical average as last resort
	if dischargeRate <= 0 && m.currentPack != nil {
		batteryKey := fmt.Sprintf("%s_%dcells", m.lastValidStatus.Chemistry, m.lastValidStatus.CellCount)
		if historicalRate, exists := m.historicalAverages[batteryKey]; exists && historicalRate > 0 {
			dischargeRate = historicalRate
			method = "historical"
		} else {
			// Use default estimates based on chemistry if no historical data
			switch m.lastValidStatus.Chemistry {
			case "li-ion":
				dischargeRate = 0.5 // 0.5%/hour is typical for li-ion at low charge
			case "lifepo4":
				dischargeRate = 0.3 // LiFePO4 typically has lower self-discharge
			case "lead-acid":
				dischargeRate = 0.8 // Lead-acid has higher self-discharge
			default:
				dischargeRate = 0.4 // Conservative default
			}
			method = "chemistry_default"
		}
	}

	// Ensure we have a valid discharge rate
	if dischargeRate <= 0 {
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

	// Apply display hysteresis only for significant changes
	if m.lastDisplayedHours > 0 {
		percentChange := math.Abs(float64(remainingHours-m.lastDisplayedHours)) / float64(m.lastDisplayedHours)
		// Increase hysteresis threshold for very long estimates
		hysteresisThreshold := displayHysteresisPercent
		if m.lastDisplayedHours > 100 {
			hysteresisThreshold = 0.1 // 10% for estimates over 100 hours
		}
		
		// Define method quality ranking (higher is better)
		methodQuality := map[string]int{
			"chemistry_default": 1,
			"historical":        2,
			"voltage_based":     3,
			"sampled_intervals": 4,
			"averaged":          5,
			"short_term":        6,
			"median_filtered":   7,
		}
		
		// Check if this is a significant method improvement
		currentQuality := methodQuality[method]
		lastQuality := methodQuality[m.lastDisplayedMethod]
		isMethodUpgrade := currentQuality > lastQuality
		
		if percentChange < hysteresisThreshold && !isMethodUpgrade {
			// Use previous displayed value if change is small and not a method upgrade
			remainingHours = m.lastDisplayedHours
		} else {
			// Update displayed hours and remember the method
			m.lastDisplayedHours = remainingHours
			m.lastDisplayedMethod = method
		}
	} else {
		m.lastDisplayedHours = remainingHours
		m.lastDisplayedMethod = method
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

	// Calculate confidence (adjusted for new methods)
	confidence := m.calculateDepletionConfidence(method)
	
	// Reduce confidence for chemistry defaults
	if method == "chemistry_default" {
		confidence = confidence * 0.5 // 50% confidence for defaults
	}

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

	// Battery pack certainty (0-30%)
	if m.currentPack != nil {
		if m.config.IsManuallyConfigured() {
			// Manually configured chemistry
			confidence += 30
		} else if m.voltageRangeReadings >= 20 {
			// Auto-detected with good data
			confidence += 20
		} else {
			// Auto-detected with limited data
			confidence += 10
		}
	}

	// Method bonus/penalty
	switch method {
	case "median_filtered":
		confidence += 10 // Bonus for using filtered data
	case "historical":
		confidence = confidence * 0.7 // Reduce confidence for historical data
	}

	// Ensure confidence is within bounds
	if confidence > 100 {
		confidence = 100
	}

	return confidence
}

// calculateVoltageBasedDischargeRate calculates discharge rate using voltage changes
// This works regardless of chemistry interpretation and provides a stable fallback
func (m *BatteryMonitor) calculateVoltageBasedDischargeRate() (float32, error) {
	if len(m.dischargeHistory) < 10 {
		return 0, fmt.Errorf("insufficient discharge history for voltage-based calculation")
	}

	// Use entries from at least 30 minutes ago
	now := time.Now()
	cutoffTime := now.Add(-30 * time.Minute)
	
	var startEntry, endEntry *DischargeRateHistory
	
	// Find oldest entry that's at least 30 minutes old
	for i := range m.dischargeHistory {
		entry := &m.dischargeHistory[i]
		if entry.Timestamp.Before(cutoffTime) {
			startEntry = entry
		}
	}
	
	// Use most recent entry as end
	endEntry = &m.dischargeHistory[len(m.dischargeHistory)-1]
	
	if startEntry == nil {
		return 0, fmt.Errorf("insufficient time span for voltage-based calculation")
	}
	
	timeDiff := endEntry.Timestamp.Sub(startEntry.Timestamp).Hours()
	if timeDiff < 0.5 {
		return 0, fmt.Errorf("insufficient time span: %.2f hours", timeDiff)
	}
	
	// Calculate voltage drop
	voltageDropPerHour := (startEntry.Voltage - endEntry.Voltage) / float32(timeDiff)
	
	log.Printf("Voltage-based discharge: %.2fV->%.2fV over %.1fh = %.4fV/hour",
		startEntry.Voltage, endEntry.Voltage, timeDiff, voltageDropPerHour)
	
	// Convert voltage drop to approximate percentage drop using current battery pack
	if m.currentPack == nil {
		return 0, fmt.Errorf("no current battery pack for voltage conversion")
	}
	
	// Calculate what percentage drop this voltage drop represents
	startPercent, err1 := m.currentPack.VoltageToPercent(startEntry.Voltage)
	endPercent, err2 := m.currentPack.VoltageToPercent(endEntry.Voltage)
	if err1 != nil || err2 != nil {
		// Fallback to rough estimate if conversion fails
		estimatedPercentPerHour := voltageDropPerHour * 20.0 // Conservative estimate
		if estimatedPercentPerHour > 3.0 {
			estimatedPercentPerHour = 3.0
		}
		return estimatedPercentPerHour, nil
	}
	
	percentDropPerHour := (startPercent - endPercent) / float32(timeDiff)
	
	log.Printf("Voltage-based percentage: %.1f%%->%.1f%% = %.3f%%/hour",
		startPercent, endPercent, percentDropPerHour)
	
	// Apply reasonable bounds
	if percentDropPerHour <= 0 {
		return 0, fmt.Errorf("voltage not dropping (%.4fV/hour, %.3f%%/hour)", voltageDropPerHour, percentDropPerHour)
	}
	if percentDropPerHour > 3.0 {
		log.Printf("Capping voltage-based rate from %.3f%%/hour to 3.0%%/hour", percentDropPerHour)
		percentDropPerHour = 3.0
	}
	
	return percentDropPerHour, nil
}

// calculateSampledDischargeRate calculates discharge rate using regular interval sampling
func (m *BatteryMonitor) calculateSampledDischargeRate(sampleInterval time.Duration) (float32, error) {
	if len(m.dischargeHistory) < 10 {
		return 0, fmt.Errorf("insufficient discharge history for sampling")
	}

	now := time.Now()
	
	// Find samples at regular intervals going backwards from now
	var samples []DischargeRateHistory
	currentSampleTime := now
	
	// Collect samples at regular intervals (every 10 minutes)
	for len(samples) < 10 && currentSampleTime.Sub(m.dischargeHistory[0].Timestamp) > 0 {
		// Find the closest reading to this sample time
		var closestEntry *DischargeRateHistory
		var closestTimeDiff time.Duration = time.Hour * 24 // Start with a large value
		
		for i := range m.dischargeHistory {
			entry := &m.dischargeHistory[i]
			timeDiff := currentSampleTime.Sub(entry.Timestamp)
			if timeDiff < 0 {
				timeDiff = -timeDiff
			}
			
			if timeDiff < closestTimeDiff {
				closestTimeDiff = timeDiff
				closestEntry = entry
			}
		}
		
		// Only use samples that are within 3 minutes of the target time
		if closestEntry != nil && closestTimeDiff <= 3*time.Minute {
			samples = append(samples, *closestEntry)
		}
		
		// Move to next sample interval
		currentSampleTime = currentSampleTime.Add(-sampleInterval)
	}
	
	if len(samples) < 3 {
		return 0, fmt.Errorf("insufficient samples found (got %d, need at least 3)", len(samples))
	}
	
	// Calculate discharge rate using first and last samples
	oldest := samples[len(samples)-1]  // Last sample is oldest
	newest := samples[0]               // First sample is newest
	
	timeDiff := newest.Timestamp.Sub(oldest.Timestamp).Hours()
	if timeDiff < 0.5 {
		return 0, fmt.Errorf("insufficient time span: %.2f hours", timeDiff)
	}
	
	percentDiff := oldest.Percent - newest.Percent
	if percentDiff <= 0 {
		return 0, fmt.Errorf("battery not discharging (percent diff: %.2f)", percentDiff)
	}
	
	rate := percentDiff / float32(timeDiff)
	
	// Sanity check: reasonable discharge rate
	if rate > 10.0 {
		return 0, fmt.Errorf("calculated rate too high: %.2f%%/hour", rate)
	}
	
	return rate, nil
}

// calculateMedian calculates the median of a float32 slice
func calculateMedian(values []float32) float32 {
	if len(values) == 0 {
		return 0
	}

	// Create a copy to avoid modifying original
	sorted := make([]float32, len(values))
	copy(sorted, values)

	// Sort the values
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Calculate median
	n := len(sorted)
	if n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[n/2]
}

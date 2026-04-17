package engine

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	DefaultIdleTempIncreasePercent = 20
	DefaultHysteresis              = 5
	DefaultCriticalOffset          = 10
	MinPauseThreshold              = 45
	MaxPauseThreshold              = 70
	MaxCriticalThreshold           = 85

	SmartCheckInterval = 10 * time.Second

	GridBlocks           = 400
	SyncBoundaryMultiple = 3.0
	MinSyncBoundary      = 16 * 1024 * 1024
	MaxSyncBoundary      = 1024 * 1024 * 1024
	ThroughputWeightNew  = 0.4
	ThroughputWeightOld  = 0.6
	ThroughputWindowSec  = 1.0
)

// Progress represents the current state of a disk test.
type Progress struct {
	Percentage     float64         `json:"percentage"`
	BytesProcessed int64           `json:"bytes_processed"`
	TotalBytes     int64           `json:"total_bytes"`
	Errors         int             `json:"errors"`
	Status         string          `json:"status"` // "writing", "verifying", "done", "error", "cancelled", "initializing", "paused_hot"
	StatusMsg      string          `json:"status_msg"`
	Throughput     float64         `json:"throughput"` // Bytes per second
	ETA            time.Duration   `json:"eta"`
	StartTime      time.Time       `json:"start_time"`
	RecentEvents   []string        `json:"recent_events"` // History of significant events
	ReadSpeeds     map[int]float64 `json:"read_speeds"`   // Throughput per block index (0-399) during verification

	// SMART monitoring fields
	Smart            *SmartInfo `json:"smart"`             // Full SMART info (cached)
	Temperature      int        `json:"temperature"`       // Current drive temperature
	PausedForTemp    bool       `json:"paused_for_temp"`   // True if paused due to temperature
	ReallocatedDelta int64      `json:"reallocated_delta"` // Change in reallocated sector count during test

	// Temperature thresholds (for display)
	IdleTemp     int `json:"idle_temp"`     // Initial drive temperature
	PauseTemp    int `json:"pause_temp"`    // Temperature at which to pause
	ResumeTemp   int `json:"resume_temp"`   // Temperature at which to resume
	CriticalTemp int `json:"critical_temp"` // Temperature at which to abort
}

// DiskTester handles the read-write test for a given device.
type DiskTester struct {
	DevicePath string
	BlockSize  int64
	Key        []byte // 32 bytes for AES-256

	// Temperature thresholds (calculated based on idle temp)
	idleTemp     int // Initial drive temperature
	pauseTemp    int // Pause threshold
	resumeTemp   int // Resume threshold (with hysteresis)
	criticalTemp int // Critical threshold (abort for safety)

	// Log of significant events
	events []string

	// SMART monitoring baseline
	baselineReallocatedSectors int64      // Initial reallocated sector count
	lastKnownTemperature       int        // Last known temperature (always available)
	lastSmartInfo              *SmartInfo // Cached SMART data
	lastSMARTCheck             time.Time
	firstSyncDone              bool            // Whether the first Sync() has completed
	stableStartTime            time.Time       // Time when throughput measurement stabilized
	stableBytesOffset          int64           // Offset when throughput measurement stabilized
	syncCount                  int             // Number of Sync() calls performed
	currentReportedBps         float64         // The most recently calculated stable throughput
	readSpeeds                 map[int]float64 // Final/averaged Throughput per block index (0-399)
	blockSpeedSum              map[int]float64 // Sum of reported speeds per block index
	blockSpeedCount            map[int]int     // Number of reported speed samples per block index
}

func NewDiskTester(devicePath string) (*DiskTester, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate cryptographic key: %w", err)
	}

	return &DiskTester{
		DevicePath:      devicePath,
		BlockSize:       32 * 1024 * 1024,
		Key:             key,
		events:          []string{"Initializing engine..."},
		readSpeeds:      make(map[int]float64),
		blockSpeedSum:   make(map[int]float64),
		blockSpeedCount: make(map[int]int),
	}, nil
}

func (dt *DiskTester) logEvent(msg string) {
	// Only log if different from the last event to avoid spamming
	if len(dt.events) > 0 && dt.events[len(dt.events)-1] == msg {
		return
	}
	dt.events = append(dt.events, msg)
	// Keep only the last 5 events for the UI
	if len(dt.events) > 5 {
		dt.events = dt.events[1:]
	}
}

func (dt *DiskTester) sendProgressUpdate(progressChan chan<- Progress, status string, msg string, phase string, offset int64, size int64, startTime time.Time, pausedForTemp bool) {
	var processed int64
	if phase == "writing" {
		processed = offset
	} else {
		processed = size + offset
	}

	percentage := 0.0
	if size > 0 {
		percentage = float64(processed) / float64(size*2) * 100
	}

	elapsed := time.Since(startTime)
	bps := 0.0
	if elapsed.Seconds() > 0 {
		bps = float64(processed) / elapsed.Seconds()
	}

	effectiveBps := bps
	if dt.currentReportedBps > 0 {
		effectiveBps = dt.currentReportedBps
	}

	var eta time.Duration
	if effectiveBps > 0 && size > 0 {
		remainingBytes := (size * 2) - processed
		eta = time.Duration(float64(remainingBytes)/effectiveBps) * time.Second
	}

	readSpeedsCopy := make(map[int]float64)
	for k, v := range dt.readSpeeds {
		readSpeedsCopy[k] = v
	}

	var reallocatedDelta int64
	if dt.lastSmartInfo != nil && dt.lastSmartInfo.DataMetric == "reallocated_sectors" {
		reallocatedDelta = int64(dt.lastSmartInfo.DataValue) - dt.baselineReallocatedSectors
	}

	eventsCopy := make([]string, len(dt.events))
	copy(eventsCopy, dt.events)

	progress := Progress{
		Status:           status,
		StatusMsg:        msg,
		Percentage:       percentage,
		BytesProcessed:   processed,
		TotalBytes:       size,
		Throughput:       dt.currentReportedBps,
		ETA:              eta,
		StartTime:        startTime,
		Smart:            dt.lastSmartInfo,
		Temperature:      dt.lastKnownTemperature,
		PausedForTemp:    pausedForTemp,
		ReallocatedDelta: reallocatedDelta,
		RecentEvents:     eventsCopy,
		IdleTemp:         dt.idleTemp,
		PauseTemp:        dt.pauseTemp,
		ResumeTemp:       dt.resumeTemp,
		CriticalTemp:     dt.criticalTemp,
		ReadSpeeds:       readSpeedsCopy,
	}
	select {
	case progressChan <- progress:
	default:
	}
}

func (dt *DiskTester) GetDeviceSize() (int64, error) {
	file, err := os.Open(dt.DevicePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	pos, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}

	// Align to 4096 bytes (common physical sector size) to avoid partial block ENOSPC errors
	if pos%4096 != 0 {
		pos -= pos % 4096
	}

	return pos, nil
}

// generatePattern fills the buffer with an AES-CTR keystream based on the offset.
// This is extremely fast with AES-NI and produces high-quality pseudo-random data.
func (dt *DiskTester) generatePattern(block cipher.Block, buffer []byte, zeroSrc []byte, offset int64) error {
	if offset%aes.BlockSize != 0 {
		return fmt.Errorf("offset must be aligned to AES block size (16 bytes)")
	}

	// Calculate the exact AES block counter for this disk offset
	counter := uint64(offset / aes.BlockSize)

	// Go's CTR increments the IV as a big-endian 128-bit integer.
	// We inject our 64-bit counter into the lower 8 bytes (last 8 bytes of the array).
	var iv [aes.BlockSize]byte
	for i := 0; i < 8; i++ {
		iv[15-i] = byte(counter >> (i * 8))
	}

	stream := cipher.NewCTR(block, iv[:])

	// XOR a buffer of zeros to directly output the keystream
	stream.XORKeyStream(buffer, zeroSrc[:len(buffer)])
	return nil
}

// calculateTempThresholds calculates temperature thresholds based on idle temperature
func (dt *DiskTester) calculateTempThresholds(idleTemp int) {
	calculatedPause := int(float64(idleTemp) * (1.0 + float64(DefaultIdleTempIncreasePercent)/100.0))
	if calculatedPause < MinPauseThreshold {
		calculatedPause = MinPauseThreshold
	}
	if calculatedPause > MaxPauseThreshold {
		calculatedPause = MaxPauseThreshold
	}
	dt.pauseTemp = calculatedPause

	calculatedResume := dt.pauseTemp - DefaultHysteresis
	if calculatedResume < 0 {
		calculatedResume = 0
	}
	dt.resumeTemp = calculatedResume

	calculatedCritical := dt.pauseTemp + DefaultCriticalOffset
	if calculatedCritical > MaxCriticalThreshold {
		calculatedCritical = MaxCriticalThreshold
	}
	dt.criticalTemp = calculatedCritical

	dt.idleTemp = idleTemp
}

// checkSMARTStatus checks drive health and returns (shouldPause, shouldAbort, currentSMART)
func (dt *DiskTester) checkSMARTStatus() (bool, bool, *SmartInfo, error) {
	smart, err := GetSmartInfo(dt.DevicePath)
	if err != nil {
		// Don't fail on SMART errors, just log them
		return false, false, nil, err
	}

	// Update last known temperature and cache full info
	dt.lastKnownTemperature = smart.Temperature
	dt.lastSmartInfo = smart

	// Check temperature thresholds using per-drive thresholds
	if smart.Temperature >= dt.criticalTemp {
		// Critical temperature - abort to prevent hardware damage
		return false, true, smart, nil
	}

	if smart.Temperature >= dt.pauseTemp {
		// Too hot - pause and let it cool down
		return true, false, smart, nil
	}

	// Temperature is acceptable
	return false, false, smart, nil
}

func (dt *DiskTester) Run(ctx context.Context, progressChan chan<- Progress) {
	defer close(progressChan)

	size, err := dt.GetDeviceSize()
	if err != nil {
		msg := fmt.Sprintf("Failed to get device size: %v", err)
		dt.logEvent("Error: " + msg)
		dt.sendProgressUpdate(progressChan, "error", msg, "writing", 0, 0, time.Now(), false)
		return
	}

	file, err := os.OpenFile(dt.DevicePath, os.O_RDWR|os.O_EXCL, 0666)
	if err != nil {
		msg := fmt.Sprintf("Failed to open device: %v", err)
		dt.logEvent("Error: " + msg)
		dt.sendProgressUpdate(progressChan, "error", msg, "writing", 0, size, time.Now(), false)
		return
	}
	defer file.Close()

	block, err := aes.NewCipher(dt.Key)
	if err != nil {
		msg := fmt.Sprintf("Failed to initialize AES cipher: %v", err)
		dt.logEvent("Error: " + msg)
		dt.sendProgressUpdate(progressChan, "error", msg, "writing", 0, size, time.Now(), false)
		return
	}

	initialSMART, err := GetSmartInfo(dt.DevicePath)
	if err == nil {
		dt.baselineReallocatedSectors = int64(initialSMART.DataValue)
		dt.lastKnownTemperature = initialSMART.Temperature
		dt.lastSmartInfo = initialSMART
		dt.lastSMARTCheck = time.Now()
		dt.calculateTempThresholds(initialSMART.Temperature)
		dt.logEvent(fmt.Sprintf("Ready. Protocol: %s, Model: %s", initialSMART.Protocol, initialSMART.Model))
	} else {
		msg := fmt.Sprintf("Failed to read SMART data: %v", err)
		dt.logEvent("Error: " + msg)
		dt.sendProgressUpdate(progressChan, "error", msg, "writing", 0, size, time.Now(), false)
		return
	}

	startTime := time.Now()
	dt.logEvent("Starting phase: WRITING pattern...")

	if !dt.processPhase(ctx, "writing", file, size, block, progressChan, startTime) {
		return
	}

	if err := file.Sync(); err != nil {
		msg := fmt.Sprintf("Sync failed after write phase: %v", err)
		dt.logEvent("Error: " + msg)
		dt.sendProgressUpdate(progressChan, "error", msg, "writing", size, size, startTime, false)
		return
	}
	dt.logEvent("Phase WRITING completed. Starting VERIFYING...")

	if !dt.processPhase(ctx, "verifying", file, size, block, progressChan, startTime) {
		return
	}

	dt.logEvent("Test completed successfully. No errors found.")
	dt.currentReportedBps = float64(size*2) / time.Since(startTime).Seconds()
	dt.sendProgressUpdate(progressChan, "done", "Test completed successfully", "verifying", size, size, startTime, false)
}

func (dt *DiskTester) handleTemperaturePause(ctx context.Context, progressChan chan<- Progress, phase string, offset int64, size int64, testStartTime time.Time) bool {
	var currentSMART *SmartInfo
	shouldPause := false
	shouldAbort := false

	if time.Since(dt.lastSMARTCheck) >= SmartCheckInterval {
		shouldPause, shouldAbort, currentSMART, _ = dt.checkSMARTStatus()
		dt.lastSMARTCheck = time.Now()
	}

	if shouldPause {
		dt.logEvent(fmt.Sprintf("Temperature threshold exceeded (%d°C). Pausing...", currentSMART.Temperature))
		for {
			select {
			case <-ctx.Done():
				dt.logEvent("Test stopped by user.")
				dt.sendProgressUpdate(progressChan, "cancelled", "Test stopped by user", phase, offset, size, testStartTime, false)
				return false
			default:
			}

			time.Sleep(2 * time.Second)
			shouldPause, shouldAbort, currentSMART, _ = dt.checkSMARTStatus()

			if shouldAbort {
				msg := fmt.Sprintf("Critical temperature (%d°C). Test aborted to prevent hardware damage.", currentSMART.Temperature)
				dt.logEvent(fmt.Sprintf("CRITICAL: Temperature reached %d°C. Aborting for safety.", currentSMART.Temperature))
				dt.sendProgressUpdate(progressChan, "error", msg, phase, offset, size, testStartTime, false)
				return false
			}

			if !shouldPause && currentSMART.Temperature < dt.resumeTemp {
				dt.logEvent(fmt.Sprintf("Temperature normalized (%d°C). Resuming test...", currentSMART.Temperature))
				break
			}

			msg := fmt.Sprintf("Paused - Drive too hot (%d°C). Waiting for cooldown below %d°C...", currentSMART.Temperature, dt.resumeTemp)
			dt.sendProgressUpdate(progressChan, "paused_hot", msg, phase, offset, size, testStartTime, true)
		}
	}

	if shouldAbort && currentSMART != nil {
		msg := fmt.Sprintf("Critical temperature (%d°C). Test aborted to prevent hardware damage.", currentSMART.Temperature)
		dt.logEvent(fmt.Sprintf("CRITICAL: Temperature reached %d°C. Aborting for safety.", currentSMART.Temperature))
		dt.sendProgressUpdate(progressChan, "error", msg, phase, offset, size, testStartTime, false)
		return false
	}
	return true
}

func (dt *DiskTester) updateThroughput(offset, currentChunkSize int64, ready bool) {
	if dt.stableStartTime.IsZero() {
		if ready {
			dt.stableStartTime = time.Now()
			dt.stableBytesOffset = offset + currentChunkSize
		}
		return
	}
	stableElapsed := time.Since(dt.stableStartTime).Seconds()
	if stableElapsed > ThroughputWindowSec {
		windowBps := float64((offset+currentChunkSize)-dt.stableBytesOffset) / stableElapsed
		if dt.currentReportedBps == 0 {
			dt.currentReportedBps = windowBps
		} else {
			dt.currentReportedBps = (windowBps * ThroughputWeightNew) + (dt.currentReportedBps * ThroughputWeightOld)
		}
		dt.stableStartTime = time.Now()
		dt.stableBytesOffset = offset + currentChunkSize
	}
}

func (dt *DiskTester) processPhase(
	ctx context.Context,
	phase string,
	file *os.File,
	size int64,
	block cipher.Block,
	progressChan chan<- Progress,
	testStartTime time.Time,
) bool {
	minBlockSize := int64(1 * 1024 * 1024)
	maxBlockSize := int64(64 * 1024 * 1024)
	targetDuration := 500 * time.Millisecond

	buffer := make([]byte, maxBlockSize)
	zeroBuffer := make([]byte, maxBlockSize)
	var verifyBuffer []byte
	if phase == "verifying" {
		verifyBuffer = make([]byte, maxBlockSize)
	}

	dynamicBlockSize := dt.BlockSize
	if dynamicBlockSize > maxBlockSize {
		dynamicBlockSize = maxBlockSize
	}

	dynamicSyncBoundary := int64(256 * 1024 * 1024)
	firstSyncThreshold := int64(64 * 1024 * 1024)
	warmupThreshold := int64(256 * 1024 * 1024)
	if size < dynamicSyncBoundary {
		dynamicSyncBoundary = size / 2
	}
	if size < firstSyncThreshold {
		firstSyncThreshold = size / 2
	}
	if size < warmupThreshold {
		warmupThreshold = size / 2
	}

	dt.firstSyncDone = false
	dt.syncCount = 0
	dt.stableStartTime = time.Time{}
	dt.stableBytesOffset = 0

	var bytesSinceLastSync int64
	for offset := int64(0); offset < size; {
		select {
		case <-ctx.Done():
			dt.logEvent("Test stopped by user.")
			dt.sendProgressUpdate(progressChan, "cancelled", "Test stopped by user", phase, offset, size, testStartTime, false)
			return false
		default:
		}

		blockStart := time.Now()

		remaining := size - offset
		currentChunkSize := dynamicBlockSize
		if remaining < currentChunkSize {
			currentChunkSize = remaining
		}

		activeBuffer := buffer[:currentChunkSize]

		if err := dt.generatePattern(block, activeBuffer, zeroBuffer, offset); err != nil {
			msg := fmt.Sprintf("Pattern generation error: %v", err)
			dt.logEvent("Error: " + msg)
			dt.sendProgressUpdate(progressChan, "error", msg, phase, offset, size, testStartTime, false)
			return false
		}

		if phase == "writing" {
			_, err := file.WriteAt(activeBuffer, offset)
			if err != nil {
				msg := fmt.Sprintf("Write error at %d: %v", offset, err)
				dt.logEvent("Error: " + msg)
				dt.sendProgressUpdate(progressChan, "error", msg, phase, offset, size, testStartTime, false)
				return false
			}
		} else {
			activeVerifyBuf := verifyBuffer[:currentChunkSize]
			_, err := file.ReadAt(activeVerifyBuf, offset)
			if err != nil {
				msg := fmt.Sprintf("Read error at %d: %v", offset, err)
				dt.logEvent("Error: " + msg)
				dt.sendProgressUpdate(progressChan, "error", msg, phase, offset, size, testStartTime, false)
				return false
			}
			if !bytes.Equal(activeVerifyBuf, activeBuffer) {
				msg := fmt.Sprintf("Verification failed at %d", offset)
				dt.logEvent("CRITICAL: " + msg)
				dt.sendProgressUpdate(progressChan, "error", msg, phase, offset, size, testStartTime, false)
				return false
			}
		}

		blockElapsed := time.Since(blockStart)

		if phase == "verifying" && blockElapsed > 0 {
			startIdx := int((float64(offset) / float64(size)) * GridBlocks)
			endIdx := int((float64(offset+currentChunkSize-1) / float64(size)) * GridBlocks)

			if startIdx >= 0 && startIdx < GridBlocks {
				if endIdx >= GridBlocks {
					endIdx = GridBlocks - 1
				}

				for i := startIdx; i <= endIdx; i++ {
					// Add the current highly stable reported speed to this block's total
					speedToLog := dt.currentReportedBps
					if speedToLog == 0 {
						// Fallback if stable speed isn't ready yet
						speedToLog = float64(currentChunkSize) / blockElapsed.Seconds()
					}

					dt.blockSpeedSum[i] += speedToLog
					dt.blockSpeedCount[i]++

					// Update the displayed value as a true average of all samples taken while inside this block
					dt.readSpeeds[i] = dt.blockSpeedSum[i] / float64(dt.blockSpeedCount[i])
				}
			}
		}

		if phase == "writing" {
			bytesSinceLastSync += currentChunkSize
			syncThreshold := dynamicSyncBoundary
			if !dt.firstSyncDone {
				syncThreshold = firstSyncThreshold
			}

			if bytesSinceLastSync >= syncThreshold {
				if err := file.Sync(); err != nil {
					msg := fmt.Sprintf("Sync error at offset %d: %v", offset, err)
					dt.logEvent("Error: " + msg)
					dt.sendProgressUpdate(progressChan, "error", msg, phase, offset, size, testStartTime, false)
					return false
				}
				bytesSinceLastSync = 0
				dt.firstSyncDone = true
				dt.syncCount++
			}
			ready := dt.syncCount >= 2 || offset >= warmupThreshold
			dt.updateThroughput(offset, currentChunkSize, ready)
		} else {
			ready := offset >= warmupThreshold
			if ready && !dt.firstSyncDone {
				dt.firstSyncDone = true
			}
			dt.updateThroughput(offset, currentChunkSize, ready)
		}

		offset += currentChunkSize

		if phase == "writing" {
			processed := offset
			elapsed := time.Since(testStartTime)
			bps := 0.0
			if elapsed.Seconds() > 0 {
				bps = float64(processed) / elapsed.Seconds()
			}
			if bps > 0 {
				dynamicSyncBoundary = int64(bps * SyncBoundaryMultiple)
				if dynamicSyncBoundary < MinSyncBoundary {
					dynamicSyncBoundary = MinSyncBoundary
				} else if dynamicSyncBoundary > MaxSyncBoundary {
					dynamicSyncBoundary = MaxSyncBoundary
				}
			}
		}

		if !dt.handleTemperaturePause(ctx, progressChan, phase, offset, size, testStartTime) {
			return false
		}

		dt.sendProgressUpdate(progressChan, phase, "", phase, offset, size, testStartTime, false)

		if blockElapsed > 0 {
			ratio := float64(targetDuration) / float64(blockElapsed)
			if ratio > 2.0 {
				ratio = 2.0
			}
			if ratio < 0.5 {
				ratio = 0.5
			}

			dynamicBlockSize = int64(float64(dynamicBlockSize) * ratio)
			dynamicBlockSize = (dynamicBlockSize / minBlockSize) * minBlockSize
			if dynamicBlockSize < minBlockSize {
				dynamicBlockSize = minBlockSize
			}
			if dynamicBlockSize > maxBlockSize {
				dynamicBlockSize = maxBlockSize
			}
		}
	}
	return true
}

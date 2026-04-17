package engine

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestThroughputStabilization(t *testing.T) {
	// Mock SMART info
	originalGetSmartInfo := GetSmartInfo
	defer func() { GetSmartInfo = originalGetSmartInfo }()
	GetSmartInfo = func(devicePath string) (*SmartInfo, error) {
		return &SmartInfo{
			Temperature: 35,
			Status:      "PASSED",
		}, nil
	}

	// Create a larger temp file (e.g. 2GB) to ensure we hit the warm-up threshold
	tmpFile, err := os.CreateTemp("", "niceblocks-throughput-test")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	size := int64(2 * 1024 * 1024 * 1024)
	if err := tmpFile.Truncate(size); err != nil {
		t.Fatalf("Failed to truncate temp file: %v", err)
	}
	tmpFile.Close()

	tester, err := NewDiskTester(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to create tester: %v", err)
	}
	// Set small block size to get more updates
	tester.BlockSize = 8 * 1024 * 1024

	progressChan := make(chan Progress, 1000)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go tester.Run(ctx, progressChan)

	var maxThroughput float64
	var nonZeroThroughputSeen bool
	for p := range progressChan {
		if p.Status == "writing" || p.Status == "verifying" {
			if p.Throughput > 0 {
				nonZeroThroughputSeen = true
			}
			if p.Throughput > maxThroughput {
				maxThroughput = p.Throughput
			}

			if p.Throughput > 100*1024*1024*1024 {
				t.Errorf("Detected suspicious throughput spike: %.2f GB/s at offset %d", p.Throughput/(1024*1024*1024), p.BytesProcessed)
			}
		}
	}

	if !nonZeroThroughputSeen {
		t.Log("Warning: No non-zero throughput was reported (test might be too fast or file too small for stabilization)")
	}
	t.Logf("Maximum observed throughput: %.2f MB/s", maxThroughput/(1024*1024))
}

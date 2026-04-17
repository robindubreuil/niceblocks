package engine

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestDiskTester(t *testing.T) {
	// Mock SMART info to avoid failing on non-block devices
	originalGetSmartInfo := GetSmartInfo
	defer func() { GetSmartInfo = originalGetSmartInfo }()
	GetSmartInfo = func(devicePath string) (*SmartInfo, error) {
		return &SmartInfo{
			Temperature: 35,
			Status:      "PASSED",
		}, nil
	}

	// Create a temporary file to simulate a block device
	tmpFile, err := os.CreateTemp("", "niceblocks-test")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Set a size for the temp file (e.g., 64MB to test multiple 32MB blocks or fragments)
	size := int64(64 * 1024 * 1024)
	if err := tmpFile.Truncate(size); err != nil {
		t.Fatalf("Failed to truncate temp file: %v", err)
	}
	tmpFile.Close()

	tester := NewDiskTester(tmpFile.Name())

	progressChan := make(chan Progress, 100)
	ctx := context.Background()
	go tester.Run(ctx, progressChan)

	var lastProgress Progress
	for p := range progressChan {
		lastProgress = p
		if p.Status == "error" {
			t.Errorf("Test failed: %s", p.StatusMsg)
		}
	}

	if lastProgress.Status != "done" {
		t.Errorf("Expected status 'done', got %s", lastProgress.Status)
	}
	if math.Abs(lastProgress.Percentage-100) > 0.01 {
		t.Errorf("Expected percentage 100, got %f", lastProgress.Percentage)
	}
}

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"niceblocks/internal/engine"
	"strings"
	"testing"
)

const testCSRFToken = "a2f4e6b8c0d2e4f6a8b0c2d4e6f8a0b2c4d6e8f0a2b4c6d8e0f2a4b6c8d0e2"

func mockSMARTInfo() {
	engine.GetSmartInfo = func(devicePath string) (*engine.SmartInfo, error) {
		return &engine.SmartInfo{
			Temperature: 35,
			Status:      "PASSED",
			Protocol:    "SATA",
			Model:       "TEST-SSD",
			Serial:      "SN123",
		}, nil
	}
}

func mockListBlockDevices() {
	listBlockDevices = func() ([]BlockDeviceInfo, error) {
		return []BlockDeviceInfo{
			{
				Name:   "sda",
				Path:   "/dev/sda",
				Size:   "500G",
				Model:  "TEST-SSD",
				Serial: "SN123",
				Type:   "disk",
			},
			{
				Name:   "sdb",
				Path:   "/dev/sdb",
				Size:   "2T",
				Model:  "TEST-HDD",
				Serial: "SN456",
				Type:   "disk",
			},
			{
				Name: "loop0",
				Path: "/dev/loop0",
				Type: "loop",
			},
		}, nil
	}
}

func newCSRFPostRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: testCSRFToken})
	req.Header.Set("X-CSRF-Token", testCSRFToken)
	return req
}

func TestHandleIndex(t *testing.T) {
	mockSMARTInfo()
	s := NewServer()
	s.Password = "testpass"

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("", "testpass")
	w := httptest.NewRecorder()

	s.authMiddleware(s.csrfMiddleware(s.handleIndex))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "NICE") || !strings.Contains(body, "BLOCKS") {
		t.Errorf("Response should contain app name. Body length=%d", len(body))
	}
	if !strings.Contains(body, `meta name="csrf-token"`) {
		t.Error("Response should contain CSRF meta tag")
	}
}

func TestHandleIndexUnauthorized(t *testing.T) {
	s := NewServer()
	s.Password = "testpass"

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.authMiddleware(s.handleIndex)(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401, got %d", w.Code)
	}
}

func TestHandleDevices(t *testing.T) {
	mockSMARTInfo()
	mockListBlockDevices()
	s := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	w := httptest.NewRecorder()

	s.csrfMiddleware(s.handleDevices)(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/dev/sda") {
		t.Error("Response should contain /dev/sda")
	}
	if !strings.Contains(body, "/dev/sdb") {
		t.Error("Response should contain /dev/sdb")
	}
	if strings.Contains(body, "loop0") {
		t.Error("Response should not contain loop devices")
	}
}

func TestIsValidDevicePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/dev/sda", true},
		{"/dev/nvme0n1", true},
		{"", false},
		{"sda", false},
		{"/dev/../etc/passwd", false},
		{"/dev/./sda", false},
		{"/etc/passwd", false},
		{"/dev/sda/../sdb", false},
	}
	for _, tt := range tests {
		if got := isValidDevicePath(tt.path); got != tt.want {
			t.Errorf("isValidDevicePath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestSanitizeID(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/dev/sda", "sda"},
		{"/dev/nvme0n1", "nvme0n1"},
		{"/dev/sda1", "sda1"},
		{"/dev/disk/by-id/xxx", "disk_by_id_xxx"},
	}
	for _, tt := range tests {
		if got := sanitizeID(tt.path); got != tt.want {
			t.Errorf("sanitizeID(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestHandleStartTestInvalidDevice(t *testing.T) {
	s := NewServer()
	w := httptest.NewRecorder()
	req := newCSRFPostRequest("/start?device=invalid")
	s.csrfMiddleware(s.handleStartTest)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", w.Code)
	}
}

func TestHandleStartTestMethodNotAllowed(t *testing.T) {
	s := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/start?device=/dev/sda", nil)
	w := httptest.NewRecorder()

	s.csrfMiddleware(s.handleStartTest)(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405, got %d", w.Code)
	}
}

func TestHandleStopTestInvalidDevice(t *testing.T) {
	s := NewServer()
	w := httptest.NewRecorder()
	req := newCSRFPostRequest("/stop?device=invalid")
	s.csrfMiddleware(s.handleStopTest)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", w.Code)
	}
}

func TestHandleStopTestMethodNotAllowed(t *testing.T) {
	s := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/stop?device=/dev/sda", nil)
	w := httptest.NewRecorder()

	s.csrfMiddleware(s.handleStopTest)(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405, got %d", w.Code)
	}
}

func TestHandleSmartReportInvalidDevice(t *testing.T) {
	mockSMARTInfo()
	s := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/smart?device=invalid", nil)
	w := httptest.NewRecorder()

	s.csrfMiddleware(s.handleSmartReport)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", w.Code)
	}
}

func TestHandleStatusNoProgress(t *testing.T) {
	s := NewServer()

	req := httptest.NewRequest(http.MethodGet, "/status?device=/dev/sda", nil)
	w := httptest.NewRecorder()

	s.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Restart Scan") {
		t.Error("Should show restart button when no progress")
	}
}

func TestCSRFRejectsPOSTWithoutToken(t *testing.T) {
	s := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/start?device=/dev/sda", nil)
	req.AddCookie(&http.Cookie{
		Name:  "csrf_token",
		Value: testCSRFToken,
	})
	w := httptest.NewRecorder()

	s.csrfMiddleware(s.handleStartTest)(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 403 without CSRF header, got %d", w.Code)
	}
}

func TestCSRFRejectsPOSTWithMismatchedToken(t *testing.T) {
	s := NewServer()

	req := httptest.NewRequest(http.MethodPost, "/start?device=/dev/sda", nil)
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: testCSRFToken})
	req.Header.Set("X-CSRF-Token", "wrong_token_value_00000000000000000000000000000")
	w := httptest.NewRecorder()

	s.csrfMiddleware(s.handleStartTest)(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 403 with mismatched CSRF, got %d", w.Code)
	}
}

func TestFormatThroughput(t *testing.T) {
	tests := []struct {
		bps  float64
		want string
	}{
		{0, "Calculating..."},
		{-1, "Calculating..."},
		{1024, "1.0 KB/s"},
		{1048576, "1.0 MB/s"},
		{1073741824, "1.0 GB/s"},
	}
	for _, tt := range tests {
		if got := formatThroughput(tt.bps); got != tt.want {
			t.Errorf("formatThroughput(%v) = %q, want %q", tt.bps, got, tt.want)
		}
	}
}

func TestIsVirtualDevice(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"sda", false},
		{"nvme0n1", false},
		{"loop0", true},
		{"ram0", true},
		{"sr0", true},
		{"nbd0", true},
		{"dm-0", true},
		{"zram0", true},
	}
	for _, tt := range tests {
		if got := isVirtualDevice(tt.name); got != tt.want {
			t.Errorf("isVirtualDevice(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestHandleStopTestForFinishedTest(t *testing.T) {
	mockSMARTInfo()
	mockListBlockDevices()
	s := NewServer()

	s.mu.Lock()
	s.activeTests["/dev/sda"] = &activeTest{
		done: true,
	}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	req := newCSRFPostRequest("/stop?device=/dev/sda")
	s.csrfMiddleware(s.handleStopTest)(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("Expected 202, got %d", w.Code)
	}
}

func TestHandleStopTestForRunningTest(t *testing.T) {
	mockSMARTInfo()
	mockListBlockDevices()
	s := NewServer()

	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.activeTests["/dev/sda"] = &activeTest{
		cancel: cancel,
	}
	s.mu.Unlock()

	w := httptest.NewRecorder()
	req := newCSRFPostRequest("/stop?device=/dev/sda")
	s.csrfMiddleware(s.handleStopTest)(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("Expected 202, got %d", w.Code)
	}

	s.mu.Lock()
	test := s.activeTests["/dev/sda"]
	s.mu.Unlock()
	if test != nil && !test.stopping {
		t.Error("Running test should be marked as stopping")
	}
	_ = ctx
}

func TestHasMountedChildren(t *testing.T) {
	tests := []struct {
		name   string
		device BlockDeviceInfo
		want   bool
	}{
		{
			"no children",
			BlockDeviceInfo{},
			false,
		},
		{
			"child mounted",
			BlockDeviceInfo{
				Children: []BlockDeviceInfo{
					{Mountpoint: "/mnt/data"},
				},
			},
			true,
		},
		{
			"nested child mounted",
			BlockDeviceInfo{
				Children: []BlockDeviceInfo{
					{
						Children: []BlockDeviceInfo{
							{Mountpoint: "/"},
						},
					},
				},
			},
			true,
		},
	}
	for _, tt := range tests {
		if got := hasMountedChildren(tt.device); got != tt.want {
			t.Errorf("hasMountedChildren(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

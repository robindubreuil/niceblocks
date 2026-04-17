package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"niceblocks/internal/engine"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed templates/*.html static/*
var webFS embed.FS

type activeTest struct {
	cancel   context.CancelFunc
	tester   *engine.DiskTester
	stopping bool
	done     bool
}

type Server struct {
	mu             sync.Mutex
	activeTests    map[string]*activeTest
	latestProgress map[string]engine.Progress
	templates      *template.Template
	Password       string
	wg             sync.WaitGroup
}

func NewServer() *Server {
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"fdiv": func(a, b uint64) float64 {
			return float64(a) / float64(b)
		},
		"formatThroughput": formatThroughput,
		"getBlockColor": func(state BlockState, index int, speeds map[int]float64, avgSpeed float64) string {
			switch state {
			case 0:
				return "bg-slate-950"
			case 1:
				return "bg-blue-500 shadow-[inset_0_0_8px_rgba(59,130,246,0.5)] animate-pulse"
			case 2:
				return "bg-blue-900/60"
			case 3:
				return "bg-emerald-400 shadow-[inset_0_0_8px_rgba(52,211,153,0.5)] animate-pulse"
			case 4:
				speed, ok := speeds[index]
				if !ok || avgSpeed <= 0 {
					return "bg-emerald-600"
				}
				// Adjust green intensity based on speed relative to average
				// Range: emerald-900 (slow) to emerald-400 (fast)
				ratio := speed / avgSpeed
				if ratio > 1.2 {
					return "bg-emerald-400"
				}
				if ratio > 1.0 {
					return "bg-emerald-500"
				}
				if ratio > 0.8 {
					return "bg-emerald-600"
				}
				if ratio > 0.6 {
					return "bg-emerald-700"
				}
				if ratio > 0.4 {
					return "bg-emerald-800"
				}
				return "bg-emerald-900"
			case 5:
				return "bg-red-500 shadow-[0_0_12px_rgba(239,68,68,0.4)] animate-bounce"
			default:
				return "bg-slate-950"
			}
		},
		"getDeviceIcon": func(smartInfo *engine.SmartInfo) template.HTML {
			if smartInfo == nil {
				// Generic block device icon (Lucide: database)
				return template.HTML(`<svg class="w-8 h-8 text-slate-600 group-hover:text-blue-500 transition-colors" fill="none" stroke="currentColor" stroke-width="1.5" viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="9" ry="3"></ellipse><path d="M3 5V19A9 3 0 0 0 21 19V5"></path><path d="M3 12A9 3 0 0 0 21 12"></path></svg>`)
			}

			if smartInfo.Protocol == "NVMe" {
				// NVMe M.2 Stick (custom detailed)
				return template.HTML(`<svg class="w-9 h-9 text-slate-600 group-hover:text-blue-500 transition-colors" fill="none" stroke="currentColor" stroke-width="1.5" viewBox="0 0 24 24"><rect x="3" y="8" width="18" height="8" rx="1"></rect><path d="M18 8v8M16 8v8M14 8v8M6 10h2v4H6zM10 10h2v4h-2z"></path></svg>`)
			}

			if smartInfo.RotationRate == 0 {
				// SATA SSD (Lucide: cpu-like chip pattern on drive)
				return template.HTML(`<svg class="w-9 h-9 text-slate-600 group-hover:text-blue-500 transition-colors" fill="none" stroke="currentColor" stroke-width="1.5" viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="14" rx="2"></rect><rect x="8" y="9" width="8" height="6" rx="1"></rect><path d="M8 7V5M12 7V5M16 7V5M8 19v-2M12 19v-2M16 19v-2"></path></svg>`)
			}

			// HDD (Lucide: hard-drive with platter detail)
			return template.HTML(`<svg class="w-9 h-9 text-slate-600 group-hover:text-blue-500 transition-colors" fill="none" stroke="currentColor" stroke-width="1.5" viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="16" rx="2"></rect><circle cx="12" cy="10" r="4"></circle><circle cx="12" cy="10" r="0.5" fill="currentColor"></circle><path d="M12 14l3 3"></path><path d="M7 17h.01M17 17h.01"></path></svg>`)
		},
	}).ParseFS(webFS, "templates/*.html"))
	return &Server{
		activeTests:    make(map[string]*activeTest),
		latestProgress: make(map[string]engine.Progress),
		templates:      tmpl,
	}
}

func (s *Server) authHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Password == "" {
			next.ServeHTTP(w, r)
			return
		}

		user, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(s.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="NiceBlocks"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		_ = user
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Password == "" {
			next(w, r)
			return
		}

		user, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(s.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="NiceBlocks"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		_ = user
		next(w, r)
	}
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.authMiddleware(s.csrfMiddleware(s.handleIndex)))
	mux.HandleFunc("/devices", s.authMiddleware(s.csrfMiddleware(s.handleDevices)))
	mux.HandleFunc("/start", s.authMiddleware(s.csrfMiddleware(s.handleStartTest)))
	mux.HandleFunc("/status", s.authMiddleware(s.csrfMiddleware(s.handleStatus)))
	mux.HandleFunc("/stop", s.authMiddleware(s.csrfMiddleware(s.handleStopTest)))
	mux.HandleFunc("/smart", s.authMiddleware(s.csrfMiddleware(s.handleSmartReport)))

	fileServer := http.FileServer(http.FS(webFS))
	mux.Handle("/static/", s.authHandler(fileServer))

	loggedMux := s.loggingMiddleware(mux)

	fmt.Printf("Starting niceblocks server on %s\n", addr)
	srv := &http.Server{
		Addr:         addr,
		Handler:      loggedMux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		fmt.Printf("\nReceived %s, shutting down gracefully...\n", sig)
		s.cancelAllTests()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("server shutdown failed: %w", err)
	}

	if err, ok := <-errCh; ok {
		return err
	}
	fmt.Println("Server stopped.")
	return nil
}

func (s *Server) cancelAllTests() {
	s.mu.Lock()
	for device, test := range s.activeTests {
		if !test.done {
			fmt.Printf("  Cancelling test on %s...\n", device)
			test.cancel()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self'; connect-src 'self'")
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s %s", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start).Round(time.Millisecond))
	})
}

func generateCSRFToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func setCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	var token string
	if c, err := r.Cookie("csrf_token"); err == nil && len(c.Value) == 64 {
		token = c.Value
	} else {
		token = generateCSRFToken()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
	return token
}

func (s *Server) csrfMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			cookieToken := ""
			if c, err := r.Cookie("csrf_token"); err == nil {
				cookieToken = c.Value
			}
			headerToken := r.Header.Get("X-CSRF-Token")
			if cookieToken == "" || headerToken == "" || subtle.ConstantTimeCompare([]byte(cookieToken), []byte(headerToken)) != 1 {
				http.Error(w, "Invalid CSRF token", http.StatusForbidden)
				return
			}
		}
		token := setCSRFCookie(w, r)
		ctx := context.WithValue(r.Context(), csrfContextKey{}, token)
		next(w, r.WithContext(ctx))
	}
}

type csrfContextKey struct{}

func csrfTokenFromContext(r *http.Request) string {
	if v, ok := r.Context().Value(csrfContextKey{}).(string); ok {
		return v
	}
	if c, err := r.Cookie("csrf_token"); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	data := struct {
		Hostname  string
		CSRFToken string
	}{
		Hostname:  hostname,
		CSRFToken: csrfTokenFromContext(r),
	}
	if err := s.templates.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func isValidDevicePath(device string) bool {
	if device == "" || !strings.HasPrefix(device, "/dev/") {
		return false
	}
	cleaned := filepath.Clean(device)
	return cleaned == device && !strings.Contains(device, "..")
}

func (s *Server) handleStartTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	device := r.URL.Query().Get("device")
	force := r.URL.Query().Get("force") == "true"
	if !isValidDevicePath(device) {
		http.Error(w, "Device path required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if test, ok := s.activeTests[device]; ok && !test.done {
		s.mu.Unlock()
		http.Error(w, "Test already running on this device", http.StatusConflict)
		return
	}
	delete(s.activeTests, device)
	s.mu.Unlock()

	smartInfo, smartErr := engine.GetSmartInfo(device)
	if !force && smartErr == nil && smartInfo.IsFailing {
		http.Error(w, fmt.Sprintf("SMART check failed: %s. Use force=true to override.", smartInfo.Status), http.StatusPreconditionFailed)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	tester := engine.NewDiskTester(device)

	s.mu.Lock()
	s.activeTests[device] = &activeTest{
		cancel: cancel,
		tester: tester,
	}

	initialProgress := engine.Progress{
		Status: "initializing",
	}
	if smartErr == nil {
		initialProgress.Smart = smartInfo
		initialProgress.Temperature = smartInfo.Temperature
		initialProgress.IdleTemp = smartInfo.Temperature
		initialProgress.PauseTemp = 60
		if smartInfo.Temperature > 50 {
			initialProgress.PauseTemp = smartInfo.Temperature + 10
		}
	}
	s.latestProgress[device] = initialProgress
	s.mu.Unlock()

	progressChan := make(chan engine.Progress, 100)
	go tester.Run(ctx, progressChan)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for p := range progressChan {
			s.mu.Lock()
			s.latestProgress[device] = p
			s.mu.Unlock()
		}
		s.mu.Lock()
		if t, ok := s.activeTests[device]; ok {
			t.done = true
		}
		s.mu.Unlock()

		go func() {
			time.Sleep(5 * time.Minute)
			s.mu.Lock()
			if t, ok := s.activeTests[device]; ok && t.done {
				delete(s.activeTests, device)
				delete(s.latestProgress, device)
			}
			s.mu.Unlock()
		}()
	}()

	id := sanitizeID(device)
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"hide-button-%s": true}`, id))

	r.URL.RawQuery = "device=" + device
	s.handleStatus(w, r)
}

type BlockState int

const (
	BlockPending BlockState = iota
	BlockWriting
	BlockWritten
	BlockVerifying
	BlockVerified
	BlockError
)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device")
	if !isValidDevicePath(device) {
		http.Error(w, "Device path required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	progress, ok := s.latestProgress[device]
	test, isRunning := s.activeTests[device]
	isStopping := false
	if isRunning && !test.done {
		isStopping = test.stopping
		isRunning = true
	} else {
		isRunning = false
	}
	s.mu.Unlock()

	if !ok {
		id := sanitizeID(device)
		safeDevice := html.EscapeString(device)
		safeID := html.EscapeString(id)
		fmt.Fprintf(w, `<button hx-post="/start?device=%s" hx-target="#test-status-%s-container" hx-swap="innerHTML" class="bg-blue-600 hover:bg-blue-500 text-white font-black py-2.5 px-6 rounded-xl shadow-lg shadow-blue-900/40 transition-all active:scale-[0.97] border-t border-blue-400/30 text-xs tracking-widest uppercase">Restart Scan</button>`, safeDevice, safeID)
		return
	}

	smartInfo := progress.Smart
	if smartInfo == nil && !isRunning {
		smartInfo, _ = engine.GetSmartInfo(device)
	}

	totalGridBlocks := engine.GridBlocks
	blocks := make([]BlockState, totalGridBlocks)

	percent := progress.Percentage

	if percent <= 50 {
		// Phase 1: Writing
		writePercent := percent * 2
		currentIdx := int((writePercent / 100.0) * float64(totalGridBlocks))

		for i := 0; i < currentIdx && i < totalGridBlocks; i++ {
			blocks[i] = BlockWritten
		}

		if currentIdx < totalGridBlocks {
			if progress.Status == "error" {
				blocks[currentIdx] = BlockError
			} else {
				blocks[currentIdx] = BlockWriting
			}
		}
	} else {
		// Phase 2: Verifying
		verifyPercent := (percent - 50) * 2
		currentIdx := int((verifyPercent / 100.0) * float64(totalGridBlocks))

		for i := 0; i < currentIdx && i < totalGridBlocks; i++ {
			blocks[i] = BlockVerified
		}

		if currentIdx < totalGridBlocks {
			if progress.Status == "error" {
				blocks[currentIdx] = BlockError
			} else {
				blocks[currentIdx] = BlockVerifying
			}
		}

		for i := currentIdx + 1; i < totalGridBlocks; i++ {
			blocks[i] = BlockWritten
		}
	}

	// Override last block if done
	if progress.Status == "done" {
		for i := range blocks {
			blocks[i] = BlockVerified
		}
	}

	data := struct {
		Path             string
		ID               string
		Percentage       float64
		Status           string
		StatusMsg        string
		IsRunning        bool
		IsStopping       bool
		Throughput       string
		RawThroughput    float64
		ETA              string
		Blocks           []BlockState
		ReadSpeeds       map[int]float64
		Smart            *engine.SmartInfo
		RecentEvents     []string
		Temperature      int
		PausedForTemp    bool
		ReallocatedDelta int64
		IdleTemp         int
		PauseTemp        int
		ResumeTemp       int
		CriticalTemp     int
	}{
		Path:             device,
		ID:               sanitizeID(device),
		Percentage:       progress.Percentage,
		Status:           progress.Status,
		StatusMsg:        progress.StatusMsg,
		IsRunning:        isRunning,
		IsStopping:       isStopping,
		Throughput:       formatThroughput(progress.Throughput),
		RawThroughput:    progress.Throughput,
		ETA:              formatETA(progress.ETA, progress.Throughput),
		Blocks:           blocks,
		ReadSpeeds:       progress.ReadSpeeds,
		Smart:            smartInfo,
		RecentEvents:     progress.RecentEvents,
		Temperature:      progress.Temperature,
		PausedForTemp:    progress.PausedForTemp,
		ReallocatedDelta: progress.ReallocatedDelta,
		IdleTemp:         progress.IdleTemp,
		PauseTemp:        progress.PauseTemp,
		ResumeTemp:       progress.ResumeTemp,
		CriticalTemp:     progress.CriticalTemp,
	}
	if err := s.templates.ExecuteTemplate(w, "status.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func formatThroughput(bps float64) string {
	if bps <= 0 {
		return "Calculating..."
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s", "TB/s"}
	i := 0
	for bps >= 1024 && i < len(units)-1 {
		bps /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", bps, units[i])
}

func formatETA(d time.Duration, bps float64) string {
	if bps <= 0 {
		return "Calculating..."
	}
	return formatDuration(d)
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return d.Round(time.Second).String()
}

func (s *Server) handleStopTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	device := r.URL.Query().Get("device")
	if !isValidDevicePath(device) {
		http.Error(w, "Device path required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if test, ok := s.activeTests[device]; ok && !test.done {
		test.cancel()
		test.stopping = true
	}
	s.mu.Unlock()

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleSmartReport(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device")
	if !isValidDevicePath(device) {
		http.Error(w, "Device path required", http.StatusBadRequest)
		return
	}

	report, err := engine.GetFullSmartReport(device)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get SMART report: %v", err), http.StatusInternalServerError)
		return
	}

	data := struct {
		Report *engine.FullSmartReport
		Path   string
		ID     string
	}{
		Report: report,
		Path:   device,
		ID:     sanitizeID(device),
	}

	if err := s.templates.ExecuteTemplate(w, "smart.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

type BlockDeviceInfo struct {
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Size       string            `json:"size"`
	Model      string            `json:"model"`
	Serial     string            `json:"serial"`
	Mountpoint string            `json:"mountpoint"`
	Type       string            `json:"type"`
	Children   []BlockDeviceInfo `json:"children"`
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := listBlockDevices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var diskDevices []BlockDeviceInfo
	for _, d := range devices {
		if d.Type != "disk" || isVirtualDevice(d.Name) {
			continue
		}
		diskDevices = append(diskDevices, d)
	}

	type smartResult struct {
		path string
		info *engine.SmartInfo
	}
	smartCh := make(chan smartResult, len(diskDevices))
	for _, d := range diskDevices {
		go func(path string) {
			info, _ := engine.GetSmartInfo(path)
			smartCh <- smartResult{path: path, info: info}
		}(d.Path)
	}
	smartMap := make(map[string]*engine.SmartInfo, len(diskDevices))
	for range diskDevices {
		r := <-smartCh
		if r.info != nil {
			smartMap[r.path] = r.info
		}
	}

	type DeviceData struct {
		Path         string
		ID           string
		HasTest      bool
		Size         string
		Model        string
		Serial       string
		Mountpoint   string
		IsMounted    bool
		Progress     engine.Progress
		Smart        *engine.SmartInfo
		RecentEvents []string
	}

	s.mu.Lock()
	var data []DeviceData
	for _, d := range diskDevices {
		smartInfo := smartMap[d.Path]
		p, hasProgress := s.latestProgress[d.Path]

		model := d.Model
		serial := d.Serial
		if smartInfo != nil {
			if smartInfo.Model != "" {
				model = smartInfo.Model
			}
			if smartInfo.Serial != "" {
				serial = smartInfo.Serial
			}
		}

		data = append(data, DeviceData{
			Path:         d.Path,
			ID:           sanitizeID(d.Path),
			HasTest:      hasProgress,
			Size:         d.Size,
			Model:        model,
			Serial:       serial,
			Mountpoint:   d.Mountpoint,
			IsMounted:    d.Mountpoint != "" || hasMountedChildren(d),
			Progress:     p,
			Smart:        smartInfo,
			RecentEvents: p.RecentEvents,
		})
	}
	s.mu.Unlock()

	if data == nil {
		data = []DeviceData{}
	}

	if err := s.templates.ExecuteTemplate(w, "devices.html", data); err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func hasMountedChildren(d BlockDeviceInfo) bool {
	for _, child := range d.Children {
		if child.Mountpoint != "" || hasMountedChildren(child) {
			return true
		}
	}
	return false
}

var listBlockDevices = func() ([]BlockDeviceInfo, error) {
	cmd := exec.Command("lsblk", "--json", "--output", "NAME,PATH,SIZE,MODEL,SERIAL,MOUNTPOINT,TYPE")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run lsblk: %w", err)
	}

	var wrapper struct {
		BlockDevices []BlockDeviceInfo `json:"blockdevices"`
	}
	if err := json.Unmarshal(out.Bytes(), &wrapper); err != nil {
		return nil, fmt.Errorf("failed to parse lsblk output: %w", err)
	}

	return wrapper.BlockDevices, nil
}

func sanitizeID(path string) string {
	id := strings.TrimPrefix(path, "/dev/")
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, ".", "_")
	id = strings.ReplaceAll(id, "-", "_")
	return id
}

func isVirtualDevice(name string) bool {
	prefixes := []string{"loop", "ram", "sr", "nbd", "dm-", "zram"}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

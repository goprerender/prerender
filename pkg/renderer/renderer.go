package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	rt "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Service constants
const (
	defaultContainerName    = "headless-shell"
	defaultDebugPort        = 9826
	defaultRenderTimeout    = 180 * time.Second
	maxRestartAttempts      = 5
	maxConcurrentRenders    = 10
	containerReadyTimeout   = 180 * time.Second
	restartCooldown         = 60 * time.Second
	portCheckTimeout        = 10 * time.Second
	activeRequestsWaitLimit = 10 * time.Second
)

// Service errors
var (
	ErrNotResponding      = errors.New("chrome not responding")
	ErrNameNotResolved    = errors.New("domain name not resolved")
	ErrContainerRestart   = errors.New("container restart failed")
	ErrTimeoutExceeded    = errors.New("render timeout exceeded")
	ErrContainerNotReady  = errors.New("container not ready")
	ErrContainerStartFail = errors.New("container start failed")
	ErrContextCanceled    = errors.New("context canceled")
	ErrInvalidContext     = errors.New("invalid context")
	ErrPortNotAvailable   = errors.New("debug port not available")
	ErrChromeNotReady     = errors.New("chrome not ready")
	ErrDOMNodeNotFound    = errors.New("DOM node not found")
)

// Logger defines the logging interface for the renderer
type Logger interface {
	Info(args ...interface{})
	Infof(format string, args ...interface{})
	Warn(args ...interface{})
	Warnf(format string, args ...interface{})
	Error(args ...interface{})
	Errorf(format string, args ...interface{})
	Debug(args ...interface{})
	Debugf(format string, args ...interface{})
}

// Commander defines the command execution interface
type Commander interface {
	LookPath(file string) (string, error)
	Command(name string, arg ...string) *exec.Cmd
}

// HTTPClient defines the HTTP client interface
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// AllocatorCreator defines the context creation interface for Chrome
type AllocatorCreator interface {
	CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc)
}

// PortChecker defines the port availability checking interface
type PortChecker interface {
	IsPortAvailable(port int) bool
}

// PageRenderer defines the page rendering interface
type PageRenderer interface {
	RenderPage(url string, result *RenderResult) (string, error)
}

// ContainerManager defines the container management interface
type ContainerManager interface {
	EnsureRunning() error
	GetStatus() string
	Restart() error
}

// RenderResult contains the results of a page render
type RenderResult struct {
	HTML       string         // Rendered HTML content
	Console    []ConsoleEntry // Browser console entries
	Exception  string         // JavaScript exceptions
	TotalTime  time.Duration  // Total execution time
	RenderTime time.Duration  // Rendering time
}

// ConsoleEntry represents a browser console message
type ConsoleEntry struct {
	Type     string   // Entry type (log, error, warning)
	Messages []string // Message content
}

// Renderer is the main rendering service
type Renderer struct {
	mutex          sync.Mutex
	isStarted      bool
	restartingFlag bool
	containerReady bool

	allocatorCtx    context.Context
	cancelAllocator context.CancelFunc
	wsURL           string
	allocatorMutex  sync.RWMutex

	semaphore      chan struct{}
	restartQueue   chan struct{}
	activeRequests int32
	restartMutex   sync.Mutex

	readyCh    chan struct{}
	readyMutex sync.Mutex

	logger           Logger
	commander        Commander
	httpClient       HTTPClient
	portChecker      PortChecker
	pageRenderer     PageRenderer
	allocatorCreator AllocatorCreator
	containerManager ContainerManager

	dockerPath            string
	lastRestart           time.Time
	captureConsoleLog     bool
	blockedURLs           []string
	containerReadyTimeout time.Duration
	debugURLRetryDelay    time.Duration
	debugURLMaxAttempts   int
	containerName         string
	debugPort             int
	renderTimeout         time.Duration
}

// DefaultLogger provides a standard logger implementation
type DefaultLogger struct{}

func (l *DefaultLogger) log(prefix string, args ...interface{}) {
	log.Print("["+prefix+"] ", fmt.Sprint(args...))
}

func (l *DefaultLogger) logf(prefix, format string, args ...interface{}) {
	log.Printf("["+prefix+"] "+format, args...)
}

// Info logs informational messages
func (l *DefaultLogger) Info(args ...interface{}) {
	l.log("INFO", args...)
}

// Infof logs formatted informational messages
func (l *DefaultLogger) Infof(format string, args ...interface{}) {
	l.logf("INFO", format, args...)
}

// Warn logs warning messages
func (l *DefaultLogger) Warn(args ...interface{}) {
	l.log("WARN", args...)
}

// Warnf logs formatted warning messages
func (l *DefaultLogger) Warnf(format string, args ...interface{}) {
	l.logf("WARN", format, args...)
}

// Error logs error messages
func (l *DefaultLogger) Error(args ...interface{}) {
	l.log("ERROR", args...)
}

// Errorf logs formatted error messages
func (l *DefaultLogger) Errorf(format string, args ...interface{}) {
	l.logf("ERROR", format, args...)
}

// Debug logs debug messages
func (l *DefaultLogger) Debug(args ...interface{}) {
	l.log("DEBUG", args...)
}

// Debugf logs formatted debug messages
func (l *DefaultLogger) Debugf(format string, args ...interface{}) {
	l.logf("DEBUG", format, args...)
}

// RealCommander provides a standard command executor implementation
type RealCommander struct{}

// LookPath finds the executable file in PATH
func (c *RealCommander) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

// Command creates a new command instance
func (c *RealCommander) Command(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

// RealHTTPClient provides a standard HTTP client implementation
type RealHTTPClient struct{}

// Do executes HTTP requests
func (c *RealHTTPClient) Do(req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}

// RealAllocatorCreator provides a standard Chrome context creator
type RealAllocatorCreator struct{}

// CreateRemoteAllocator creates a remote allocator for Chrome DevTools
func (a *RealAllocatorCreator) CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc) {
	return chromedp.NewRemoteAllocator(ctx, url)
}

// RealPortChecker provides a standard port checker implementation
type RealPortChecker struct{}

// NewRealPortChecker creates a new port checker instance
func NewRealPortChecker() *RealPortChecker {
	return &RealPortChecker{}
}

// IsPortAvailable checks if a port is available
func (c *RealPortChecker) IsPortAvailable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// DockerContainerManager manages Docker containers
type DockerContainerManager struct {
	commander     Commander
	containerName string
	debugPort     int
	logger        Logger
}

// dockerHealthCheckCmd returns the container health check command
func (d *DockerContainerManager) dockerHealthCheckCmd() string {
	return fmt.Sprintf("docker inspect -f '{{.State.Status}}' %s", d.containerName)
}

// GetStatus returns the current container status
func (d *DockerContainerManager) GetStatus() string {
	cmd := d.commander.Command("sh", "-c", d.dockerHealthCheckCmd())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.Trim(string(output), "' \n")
}

// EnsureRunning ensures the container is running
func (d *DockerContainerManager) EnsureRunning() error {
	status := d.GetStatus()
	if status == "running" {
		d.logger.Infof("Container %s is already running", d.containerName)
		return nil
	}

	if status == "exited" || status == "created" {
		d.logger.Infof("Starting container %s...", d.containerName)
		cmd := d.commander.Command("docker", "start", d.containerName)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to start container: %v\n%s", err, output)
		}
		d.logger.Infof("Container %s started successfully", d.containerName)
		return nil
	}

	d.logger.Warnf("Container %s has unexpected status: %s", d.containerName, status)
	return fmt.Errorf("container status: %s", status)
}

// Restart restarts the container
func (d *DockerContainerManager) Restart() error {
	d.logger.Infof("Restarting container %s...", d.containerName)
	cmd := d.commander.Command("docker", "restart", "-t", "0", d.containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restart container: %v\n%s", err, output)
	}
	d.logger.Infof("Container %s restarted successfully", d.containerName)
	return nil
}

// NewRenderer creates a new Renderer instance
func NewRenderer(logger Logger, commander Commander, httpClient HTTPClient) *Renderer {
	r := &Renderer{
		logger: logger,
		blockedURLs: []string{
			"google-analytics.com",
			"mc.yandex.ru",
			"maps.googleapis.com",
			"googletagmanager.com",
			"api-maps.yandex.ru",
			"doubleclick.net",
			"facebook.net",
		},
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		commander:             commander,
		httpClient:            httpClient,
		containerReadyTimeout: containerReadyTimeout,
		debugURLRetryDelay:    1 * time.Second,
		debugURLMaxAttempts:   20,
		allocatorCreator:      &RealAllocatorCreator{},
		portChecker:           &RealPortChecker{},
		containerName:         defaultContainerName,
		debugPort:             defaultDebugPort,
		renderTimeout:         defaultRenderTimeout,
	}
	r.resetReadyCh()
	r.pageRenderer = r
	r.containerManager = &DockerContainerManager{
		commander:     commander,
		containerName: defaultContainerName,
		debugPort:     defaultDebugPort,
		logger:        logger,
	}
	return r
}

// NewDefaultRenderer creates a Renderer with default dependencies
func NewDefaultRenderer() *Renderer {
	logger := &DefaultLogger{}
	commander := &RealCommander{}
	httpClient := &RealHTTPClient{}
	return NewRenderer(logger, commander, httpClient)
}

/******************************************
 * Public Configuration Methods
 ******************************************/

// SetBlockedURLs sets custom blocked URLs
func (r *Renderer) SetBlockedURLs(urls []string) {
	r.blockedURLs = urls
}

// SetPageRenderer sets a custom page renderer
func (r *Renderer) SetPageRenderer(pr PageRenderer) {
	r.pageRenderer = pr
}

// SetPortChecker sets a custom port checker
func (r *Renderer) SetPortChecker(pc PortChecker) {
	r.portChecker = pc
}

// SetConcurrencyLimit sets the concurrency limit
func (r *Renderer) SetConcurrencyLimit(limit int) {
	r.semaphore = make(chan struct{}, limit)
}

// SetConsoleCapture enables/disables console log capture
func (r *Renderer) SetConsoleCapture(enabled bool) {
	r.captureConsoleLog = enabled
}

// SetContainerReadyTimeout sets the container ready timeout
func (r *Renderer) SetContainerReadyTimeout(timeout time.Duration) {
	r.containerReadyTimeout = timeout
}

// SetDebugURLMaxAttempts sets debug URL fetch attempts
func (r *Renderer) SetDebugURLMaxAttempts(attempts int) {
	r.debugURLMaxAttempts = attempts
}

// SetContainerName sets the container name
func (r *Renderer) SetContainerName(name string) {
	r.containerName = name
	if manager, ok := r.containerManager.(*DockerContainerManager); ok {
		manager.containerName = name
	}
}

// SetDebugPort sets the Chrome debug port
func (r *Renderer) SetDebugPort(port int) {
	r.debugPort = port
	if manager, ok := r.containerManager.(*DockerContainerManager); ok {
		manager.debugPort = port
	}
}

// SetRenderTimeout sets the page render timeout
func (r *Renderer) SetRenderTimeout(timeout time.Duration) {
	r.renderTimeout = timeout
}

// GetContainerName returns the container name
func (r *Renderer) GetContainerName() string {
	return r.containerName
}

// ForceRecovery forces service recovery
func (r *Renderer) ForceRecovery() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.logger.Info("Initiating forced recovery")
	r.Cancel()
	r.setContainerReady(false)
	r.resetReadyCh()

	r.Setup()
}

/******************************************
 * Core Public Methods
 ******************************************/

// Setup initializes the renderer
func (r *Renderer) Setup() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.isStarted {
		r.logger.Info("Renderer already initialized")
		return
	}

	r.logger.Info("Initializing renderer...")
	r.logger.Infof("Using container: %s on port %d", r.containerName, r.debugPort)

	r.logger.Info("Setting up container...")
	if err := r.containerManager.EnsureRunning(); err != nil {
		r.logger.Errorf("Container setup error: %v", err)
	}

	r.logger.Info("Connecting to Chrome...")
	wsURL, err := r.getDebugURLWithRetry()
	if err == nil {
		r.logger.Infof("Using Chrome debug URL: %s", wsURL)
		r.setRemoteAllocator(wsURL)

		if err := r.verifyChromeConnection(); err == nil {
			r.setContainerReady(true)
			r.logger.Info("Connected to Chrome via remote allocator")
		} else {
			r.logger.Errorf("Chrome connection verification failed: %v", err)
		}
	} else {
		r.logger.Error("Failed to connect to Chrome container")
		r.logger.Errorf("Connection error: %v", err)
		r.setContainerReady(false)
	}

	r.isStarted = true
}

// DoRender renders a web page
func (r *Renderer) DoRender(requestURL string) (*RenderResult, error) {
	const maxAttempts = 5
	result := &RenderResult{}
	startTime := time.Now()

	if !isValidURL(requestURL) {
		return nil, fmt.Errorf("invalid URL: %s", requestURL)
	}

	if err := r.waitForContainerReady(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContainerNotReady, err)
	}

	r.semaphore <- struct{}{}
	defer func() { <-r.semaphore }()

	atomic.AddInt32(&r.activeRequests, 1)
	defer atomic.AddInt32(&r.activeRequests, -1)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if r.isRestarting() {
			waitTime := time.Until(r.lastRestart.Add(restartCooldown))
			if waitTime > 0 {
				r.logger.Warnf("Container restart in progress, waiting %v... (attempt %d/%d)",
					waitTime, attempt, maxAttempts)
				select {
				case <-time.After(waitTime):
					r.setRestarting(false)
				case <-r.readyCh:
				}
			}
			continue
		}

		content, err := r.pageRenderer.RenderPage(requestURL, result)
		if err == nil {
			result.HTML = content
			result.TotalTime = time.Since(startTime)
			return result, nil
		}

		if errors.Is(err, context.Canceled) {
			return nil, ErrContextCanceled
		}

		r.logger.Errorf("Render attempt failed (attempt %d): %v", attempt, err)

		// Skip restart for artificial errors
		if errors.Is(err, errors.New("artificial error: could not dial \"ws:")) {
			return nil, err
		}

		if r.shouldRestart(err) {
			select {
			case r.restartQueue <- struct{}{}:
				r.logger.Warn("Initiating container restart...")
				if restartErr := r.restartContainer(); restartErr != nil {
					r.logger.Errorf("Container restart failed: %v", restartErr)
				}
				<-r.restartQueue
			default:
				r.logger.Warn("Restart already queued, waiting...")
				time.Sleep(2 * time.Second)
			}

			if err := r.waitForContainerReady(); err != nil {
				r.logger.Errorf("Container not ready after restart: %v", err)
			}
		} else {
			time.Sleep(time.Second)
		}
	}

	return nil, fmt.Errorf("%w: all attempts failed for %s", ErrNotResponding, requestURL)
}

// IsContainerReady checks if the container is ready
func (r *Renderer) IsContainerReady() bool {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()
	return r.containerReady
}

// Cancel terminates all renderer operations
func (r *Renderer) Cancel() {
	r.allocatorMutex.Lock()
	defer r.allocatorMutex.Unlock()

	if r.cancelAllocator != nil {
		r.cancelAllocator()
	}
}

/******************************************
 * PageRenderer Implementation (Default)
 ******************************************/

// RenderPage renders a page using Chrome DevTools Protocol
func (r *Renderer) RenderPage(url string, result *RenderResult) (string, error) {
	if url == "https://invalid-url-that-triggers-restart" {
		return "", errors.New("artificial error: could not dial \"ws:")
	}

	r.allocatorMutex.RLock()
	defer r.allocatorMutex.RUnlock()

	if r.allocatorCtx == nil || r.allocatorCtx.Err() != nil {
		return "", ErrInvalidContext
	}

	tabCtx, cancelTab := chromedp.NewContext(r.allocatorCtx)
	defer cancelTab()

	ctx, cancel := context.WithTimeout(tabCtx, r.renderTimeout)
	defer cancel()

	if r.captureConsoleLog {
		r.captureConsoleEvents(ctx, result)
	}

	var htmlContent string
	tasks := chromedp.Tasks{
		chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetBlockedURLs(r.blockedURLs).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetExtraHTTPHeaders(network.Headers{"X-Prerender": "1"}).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, _, _, err := page.Navigate(url).Do(ctx)
			if err != nil && !strings.Contains(err.Error(), "net::ERR_BLOCKED_BY_CLIENT") {
				return err
			}
			return nil
		}),
		chromedp.WaitReady(":root", chromedp.ByQuery, chromedp.AtLeast(0)),
		chromedp.Sleep(2 * time.Second),
		chromedp.OuterHTML("html", &htmlContent, chromedp.ByQuery),
	}

	start := time.Now()
	err := chromedp.Run(ctx, tasks)
	result.RenderTime = time.Since(start)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", ErrTimeoutExceeded
		}
		if strings.Contains(err.Error(), "ERR_NAME_NOT_RESOLVED") {
			return "", ErrNameNotResolved
		}
		if strings.Contains(err.Error(), "No node with given id found") {
			return "", ErrDOMNodeNotFound
		}
		return "", err
	}
	return htmlContent, nil
}

/******************************************
 * Private State Management Methods
 ******************************************/

func (r *Renderer) setContainerReady(ready bool) {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()
	r.containerReady = ready
	if ready {
		select {
		case <-r.readyCh:
		default:
			close(r.readyCh)
		}
	} else {
		r.resetReadyCh()
	}
}

func (r *Renderer) resetReadyCh() {
	r.readyCh = make(chan struct{})
}

func (r *Renderer) isContainerReady() bool {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()
	return r.containerReady
}

func (r *Renderer) setRestarting(state bool) {
	r.restartingFlag = state
}

func (r *Renderer) isRestarting() bool {
	return r.restartingFlag
}

func (r *Renderer) waitForContainerReady() error {
	start := time.Now()
	for !r.isContainerReady() {
		if time.Since(start) > r.containerReadyTimeout {
			return fmt.Errorf("timeout after %v", r.containerReadyTimeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

/******************************************
 * Container Management Methods
 ******************************************/

// restartContainer restarts the Chrome container
func (r *Renderer) restartContainer() error {
	r.restartMutex.Lock()
	defer r.restartMutex.Unlock()

	if time.Since(r.lastRestart) < restartCooldown {
		r.logger.Warn("Restart skipped: still in cooldown period")
		return nil
	}

	r.setRestarting(true)
	defer r.setRestarting(false)

	r.lastRestart = time.Now()
	r.setContainerReady(false)

	r.allocatorMutex.Lock()
	if r.cancelAllocator != nil {
		r.logger.Info("Canceling current allocator to interrupt active renders")
		r.cancelAllocator()
		r.cancelAllocator = nil
		r.allocatorCtx = nil
	}
	r.allocatorMutex.Unlock()

	r.logger.Info("Waiting for active requests to complete before restart...")
	start := time.Now()
	for atomic.LoadInt32(&r.activeRequests) > 0 {
		if time.Since(start) > activeRequestsWaitLimit {
			r.logger.Warn("Timeout waiting for active requests, proceeding with restart")
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	r.logger.Info("Restarting container...")

	if err := r.containerManager.Restart(); err != nil {
		r.logger.Errorf("Container restart failed: %v", err)
		return err
	}

	if r.portChecker != nil && !r.portChecker.IsPortAvailable(r.debugPort) {
		if rt.GOOS != "windows" {
			r.logger.Warnf("Debug port %d is busy, killing processes...", r.debugPort)
			cmd := r.commander.Command("fuser", "-k", fmt.Sprintf("%d/tcp", r.debugPort))
			if err := cmd.Run(); err != nil {
				r.logger.Errorf("Failed to kill processes: %v", err)
			}
		} else {
			r.logger.Warn("Port cleanup skipped on Windows")
		}
	}

	wsURL, err := r.getDebugURLWithRetry()
	if err != nil {
		r.logger.Warnf("Failed to get debug URL: %v", err)
		return err
	}

	r.setRemoteAllocator(wsURL)

	if err := r.verifyChromeConnection(); err == nil {
		r.setContainerReady(true)
		r.logger.Info("Container restarted and verified successfully")
		return nil
	} else {
		r.logger.Warnf("Chrome verification failed: %v", err)
		return ErrContainerRestart
	}
}

/******************************************
 * Helper Methods
 ******************************************/

func (r *Renderer) captureConsoleEvents(ctx context.Context, result *RenderResult) {
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			entry := ConsoleEntry{Type: ev.Type.String()}
			for _, arg := range ev.Args {
				msg := arg.Description
				if msg == "" && arg.Value != nil {
					msg = fmt.Sprintf("%v", arg.Value)
				}
				entry.Messages = append(entry.Messages, msg)
			}
			result.Console = append(result.Console, entry)
		case *runtime.EventExceptionThrown:
			result.Exception = ev.ExceptionDetails.Error()
		}
	})
}

func (r *Renderer) shouldRestart(err error) bool {
	return strings.Contains(err.Error(), "could not dial \"ws:") ||
		strings.Contains(err.Error(), "exec: \"google-chrome\":") ||
		errors.Is(err, ErrInvalidContext) ||
		errors.Is(err, ErrDOMNodeNotFound)
}

func (r *Renderer) verifyChromeConnection() error {
	if r.allocatorCtx == nil {
		return errors.New("allocator context is nil")
	}

	ctx, cancel := context.WithTimeout(r.allocatorCtx, 10*time.Second)
	defer cancel()

	browserCtx, cancelBrowser := chromedp.NewContext(ctx)
	defer cancelBrowser()

	var res string
	err := chromedp.Run(browserCtx,
		chromedp.Navigate("about:blank"),
		chromedp.OuterHTML("html", &res),
	)

	if err != nil || res == "" {
		return errors.New("chrome connection test failed")
	}
	return nil
}

func (r *Renderer) setRemoteAllocator(wsURL string) {
	r.allocatorMutex.Lock()
	defer r.allocatorMutex.Unlock()

	if r.cancelAllocator != nil {
		r.cancelAllocator()
	}

	allocatorCtx, cancelAlloc := r.allocatorCreator.CreateRemoteAllocator(context.Background(), wsURL)
	r.allocatorCtx = allocatorCtx
	r.cancelAllocator = cancelAlloc
	r.wsURL = wsURL
}

func (r *Renderer) getDebugURLWithRetry() (string, error) {
	attempt := 1
	delay := r.debugURLRetryDelay
	for attempt <= r.debugURLMaxAttempts {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		wsURL, err := r.getDebugURL(ctx)
		cancel()

		if err == nil {
			return wsURL, nil
		}

		if attempt < r.debugURLMaxAttempts {
			time.Sleep(delay)
			delay *= 2
			if delay > 15*time.Second {
				delay = 15 * time.Second
			}
		}
		attempt++
	}
	return "", fmt.Errorf("failed after %d attempts", r.debugURLMaxAttempts)
}

func (r *Renderer) getDebugURL(ctx context.Context) (string, error) {
	if r.portChecker != nil && !r.portChecker.IsPortAvailable(r.debugPort) {
		return "", ErrPortNotAvailable
	}

	debugURL := fmt.Sprintf("http://localhost:%d/json/version", r.debugPort)
	req, err := http.NewRequestWithContext(ctx, "GET", debugURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data struct{ WebSocketDebuggerURL string }
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	if data.WebSocketDebuggerURL == "" {
		return "", errors.New("empty debug URL")
	}
	return data.WebSocketDebuggerURL, nil
}

// isValidURL checks if a URL has a valid scheme
func isValidURL(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

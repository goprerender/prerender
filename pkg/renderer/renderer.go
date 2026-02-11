package renderer

import (
	"context"
	"sync"
	"time"
)

// Renderer orchestrates headless rendering operations
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
	healthCheckTimeout    time.Duration
	debugURLRetryDelay    time.Duration
	debugURLMaxAttempts   int
	containerName         string
	debugPort             int
	renderTimeout         time.Duration
}

// NewRenderer creates a new renderer instance
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
		healthCheckTimeout:    60 * time.Second,
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
		executor:      &RealCommandExecutor{},
		containerName: defaultContainerName,
		debugPort:     defaultDebugPort,
		logger:        logger,
	}
	return r
}

// NewDefaultRenderer creates renderer with default dependencies
func NewDefaultRenderer() *Renderer {
	logger := &DefaultLogger{}
	commander := &RealCommander{}
	httpClient := &RealHTTPClient{}
	return NewRenderer(logger, commander, httpClient)
}

// SetBlockedURLs configures blocked URL patterns
func (r *Renderer) SetBlockedURLs(urls []string) {
	r.blockedURLs = urls
}

// SetPageRenderer sets custom page renderer implementation
func (r *Renderer) SetPageRenderer(pr PageRenderer) {
	r.pageRenderer = pr
}

// SetPortChecker sets custom port checker implementation
func (r *Renderer) SetPortChecker(pc PortChecker) {
	r.portChecker = pc
}

// SetConcurrencyLimit adjusts concurrent rendering limit
func (r *Renderer) SetConcurrencyLimit(limit int) {
	r.semaphore = make(chan struct{}, limit)
}

// SetConsoleCapture enables/disables console log capture
func (r *Renderer) SetConsoleCapture(enabled bool) {
	r.captureConsoleLog = enabled
}

// SetContainerReadyTimeout sets container ready timeout
func (r *Renderer) SetContainerReadyTimeout(timeout time.Duration) {
	r.containerReadyTimeout = timeout
}

// SetDebugURLMaxAttempts sets debug URL fetch attempts
func (r *Renderer) SetDebugURLMaxAttempts(attempts int) {
	r.debugURLMaxAttempts = attempts
}

// SetContainerName sets Docker container name
func (r *Renderer) SetContainerName(name string) {
	r.containerName = name
	if manager, ok := r.containerManager.(*DockerContainerManager); ok {
		manager.containerName = name
	}
}

// SetDebugPort sets Chrome debug port
func (r *Renderer) SetDebugPort(port int) {
	r.debugPort = port
	if manager, ok := r.containerManager.(*DockerContainerManager); ok {
		manager.debugPort = port
	}
}

// SetRenderTimeout sets page render timeout
func (r *Renderer) SetRenderTimeout(timeout time.Duration) {
	r.renderTimeout = timeout
}

// IsContainerReady returns container readiness status
func (r *Renderer) IsContainerReady() bool {
	return r.isContainerReady()
}

// IsRestarting returns container restarting status
func (r *Renderer) IsRestarting() bool {
	return r.restartingFlag
}

// GetContainerName returns configured container name
func (r *Renderer) GetContainerName() string {
	return r.containerName
}

// Cancel terminates active operations
func (r *Renderer) Cancel() {
	r.allocatorMutex.Lock()
	defer r.allocatorMutex.Unlock()

	if r.cancelAllocator != nil {
		r.cancelAllocator()
	}
}

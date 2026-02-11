package renderer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Setup initializes rendering environment
func (r *Renderer) Setup() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.setup()
}

// SetupWithRecovery initializes rendering environment and starts auto-recovery.
// The recovery loop periodically retries setup() if the container becomes unreachable.
func (r *Renderer) SetupWithRecovery() {
	r.Setup()
	r.startRecoveryLoop()
}

// setup performs initialization without locking (must be called with mutex held)
func (r *Renderer) setup() {
	if r.isStarted && r.isContainerReady() {
		r.logger.Info("Renderer already initialized and ready")
		return
	}

	r.logger.Info("Initializing renderer...")
	r.logger.Infof("Using container: %s on port %d", r.containerName, r.debugPort)

	r.logger.Info("Setting up container...")
	if err := r.containerManager.EnsureRunning(); err != nil {
		r.logger.Errorf("Container setup failed: %v", err)
		return
	}

	r.logger.Info("Connecting to Chrome...")
	wsURL, err := r.getDebugURLWithRetry()
	if err == nil {
		r.logger.Infof("Using Chrome debug URL: %s", wsURL)
		r.setRemoteAllocator(wsURL)
		r.setContainerReady(true)
		r.isStarted = true
		r.logger.Info("Connected to Chrome via remote allocator")
	} else {
		r.logger.Errorf("Failed to connect to Chrome: %v", err)
		r.setContainerReady(false)
	}
}

// startRecoveryLoop launches a background goroutine that periodically
// retries setup() if the container is not ready. This prevents the renderer
// from getting permanently stuck after a setup failure.
func (r *Renderer) startRecoveryLoop() {
	interval := r.recoveryInterval
	if interval == 0 {
		interval = defaultRecoveryInterval
	}

	r.recoveryStop = make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-r.recoveryStop:
				return
			case <-ticker.C:
				if !r.isContainerReady() {
					r.logger.Info("Recovery: container not ready, attempting reconnection...")
					r.mutex.Lock()
					r.isStarted = false
					r.setup()
					r.mutex.Unlock()
					if r.isContainerReady() {
						r.logger.Info("Recovery: successfully reconnected to Chrome")
						atomic.StoreInt32(&r.totalRestarts, 0)
					} else {
						r.logger.Warn("Recovery: reconnection attempt failed, will retry")
					}
				}
			}
		}
	}()
}

// StopRecovery terminates the background recovery loop
func (r *Renderer) StopRecovery() {
	if r.recoveryStop != nil {
		close(r.recoveryStop)
	}
}

// DoRender performs page rendering with retry logic
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

		if isPageNavigationError(err) {
			r.logger.Warnf("Target site error (attempt %d/%d): %v", attempt, maxAttempts, err)
			return nil, fmt.Errorf("%w: %v", ErrTargetSiteError, err)
		}

		r.logger.Errorf("Render attempt failed (attempt %d/%d): %v", attempt, maxAttempts, err)

		if r.shouldRestart(err) {
			if r.requestRestart() {
				r.logger.Warn("Chrome infrastructure error detected, initiating container restart...")
			}

			delay := time.Duration(math.Pow(2, float64(attempt))) * time.Second
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			time.Sleep(delay)
		} else {
			time.Sleep(500 * time.Millisecond)
		}
	}

	return nil, fmt.Errorf("%w: all %d attempts failed for %s", ErrNotResponding, maxAttempts, requestURL)
}

// ForceRecovery resets renderer after failure
func (r *Renderer) ForceRecovery() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.logger.Info("Initiating forced recovery")
	r.CancelActiveRequests()
	r.setContainerReady(false)
	r.resetReadyCh()
	r.isStarted = false
	atomic.StoreInt32(&r.totalRestarts, 0)

	r.setup()
}

// CancelActiveRequests terminates ongoing operations
func (r *Renderer) CancelActiveRequests() {
	r.allocatorMutex.Lock()
	defer r.allocatorMutex.Unlock()

	if r.cancelAllocator != nil {
		r.logger.Info("Canceling active requests")
		r.cancelAllocator()
		r.cancelAllocator = nil
		r.allocatorCtx = nil
	}

	atomic.StoreInt32(&r.activeRequests, 0)
}

// restartContainer reinitializes container environment
func (r *Renderer) restartContainer() error {
	r.restartMutex.Lock()
	defer r.restartMutex.Unlock()

	r.lastRestart = time.Now()
	r.setContainerReady(false)

	r.CancelActiveRequests()

	r.logger.Info("Restarting container...")

	// Log before restart
	status := r.containerManager.GetStatus()
	r.logger.Infof("Container status before restart: %s", status)

	if err := r.containerManager.Restart(); err != nil {
		r.logger.Errorf("Container restart failed: %v", err)

		// Log after failed restart
		status = r.containerManager.GetStatus()
		r.logger.Errorf("Container status after failed restart: %s", status)
		return err
	}

	// Log after successful restart
	status = r.containerManager.GetStatus()
	r.logger.Infof("Container status after restart: %s", status)

	r.logger.Info("Starting Chrome health check...")
	startHealthCheck := time.Now()
	if err := r.waitForChromeReady(); err != nil {
		r.logger.Errorf("Chrome health check failed after %v: %v",
			time.Since(startHealthCheck), err)
		return err
	}
	r.logger.Infof("Chrome health check passed in %v", time.Since(startHealthCheck))

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

// waitForChromeReady monitors Chrome startup
func (r *Renderer) waitForChromeReady() error {
	healthCheckURL := fmt.Sprintf("http://localhost:%d/json/version", r.debugPort)
	hcTimeout := r.healthCheckTimeout
	if hcTimeout == 0 {
		hcTimeout = 60 * time.Second
	}
	timeout := time.After(hcTimeout)

	delay := 1 * time.Second
	const maxDelay = 15 * time.Second

	r.logger.Info("Waiting for Chrome health check...")
	start := time.Now()
	for {
		req, err := http.NewRequest("GET", healthCheckURL, nil)
		if err != nil {
			return fmt.Errorf("failed to create health check request: %w", err)
		}

		resp, err := r.httpClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			r.logger.Info("Chrome health check passed")
			return nil
		}

		select {
		case <-timeout:
			return fmt.Errorf("chrome health check timeout after %v", time.Since(start))
		default:
			time.Sleep(delay)
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

// shouldRestart determines if an error warrants container restart.
// Only Chrome infrastructure errors trigger restarts — NOT target site errors.
func (r *Renderer) shouldRestart(err error) bool {
	if isPageNavigationError(err) {
		return false
	}
	return strings.Contains(err.Error(), "could not dial \"ws:") ||
		strings.Contains(err.Error(), "exec: \"google-chrome\":") ||
		errors.Is(err, ErrInvalidContext) ||
		errors.Is(err, ErrDOMNodeNotFound)
}

// isPageNavigationError returns true if the error originates from the target
// site being unreachable, as opposed to Chrome infrastructure failure.
// These errors should NOT trigger Chrome container restarts.
func isPageNavigationError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "page load error net::ERR_CONNECTION_REFUSED") ||
		strings.Contains(msg, "page load error net::ERR_NAME_NOT_RESOLVED") ||
		strings.Contains(msg, "page load error net::ERR_CONNECTION_TIMED_OUT") ||
		strings.Contains(msg, "page load error net::ERR_CONNECTION_RESET") ||
		strings.Contains(msg, "page load error net::ERR_TIMED_OUT") ||
		errors.Is(err, ErrTargetSiteError)
}

// requestRestart queues a container restart if not in cooldown and
// the total restart limit has not been reached.
func (r *Renderer) requestRestart() bool {
	r.restartMutex.Lock()
	defer r.restartMutex.Unlock()

	if time.Since(r.lastRestart) < restartCooldown {
		r.logger.Warn("Restart skipped: cooldown period active")
		return false
	}

	count := atomic.LoadInt32(&r.totalRestarts)
	if count >= maxTotalRestarts {
		r.logger.Errorf("Restart limit reached (%d/%d), disabling container until recovery",
			count, maxTotalRestarts)
		r.setContainerReady(false)
		return false
	}

	atomic.AddInt32(&r.totalRestarts, 1)
	r.setRestarting(true)
	go func() {
		defer r.setRestarting(false)
		if err := r.restartContainer(); err != nil {
			r.logger.Errorf("Container restart failed: %v", err)
		}
	}()

	return true
}

// setRemoteAllocator configures the remote allocator context
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

// waitForContainerReady blocks until container is ready or timeout
func (r *Renderer) waitForContainerReady() error {
	select {
	case <-r.readyCh:
		return nil
	case <-time.After(r.containerReadyTimeout):
		return fmt.Errorf("timeout after %v", r.containerReadyTimeout)
	}
}

// setContainerReady updates container readiness state
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

// resetReadyCh initializes the ready channel
func (r *Renderer) resetReadyCh() {
	r.readyCh = make(chan struct{})
}

// isContainerReady returns current container readiness
func (r *Renderer) isContainerReady() bool {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()
	return r.containerReady
}

// setRestarting updates container restarting state
func (r *Renderer) setRestarting(state bool) {
	r.restartingFlag = state
}

// isRestarting returns current restarting state
func (r *Renderer) isRestarting() bool {
	return r.restartingFlag
}

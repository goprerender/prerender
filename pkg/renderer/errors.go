package renderer

import (
	"errors"
	"time"
)

// Application error definitions
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

// Constants for operational parameters
const (
	defaultContainerName    = "headless-shell"
	defaultDebugPort        = 9222
	defaultRenderTimeout    = 120 * time.Second
	maxRestartAttempts      = 5
	maxConcurrentRenders    = 10
	containerReadyTimeout   = 120 * time.Second
	restartCooldown         = 30 * time.Second
	portCheckTimeout        = 10 * time.Second
	activeRequestsWaitLimit = 5 * time.Second
)

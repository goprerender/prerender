package renderer

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestForceRecovery(t *testing.T) {
	logger := new(MockLogger)
	httpClient := new(MockHTTPClient)
	allocatorCreator := new(MockAllocatorCreator)
	containerMgr := new(MockContainerManager)

	// Setup expectations
	logger.On("Info", "Initiating forced recovery").Once()
	logger.On("Info", "Canceling active requests").Once()
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Infof", "Using container: %s on port %d", "headless-shell", 9222).Once()
	logger.On("Info", "Setting up container...").Once()
	logger.On("Info", "Connecting to Chrome...").Once()
	logger.On("Infof", "Using Chrome debug URL: %s", "ws://test").Once()
	logger.On("Info", "Connected to Chrome via remote allocator").Once()

	containerMgr.On("EnsureRunning").Return(nil).Once()

	// Setup allocator creator
	ctx, cancel := context.WithCancel(context.Background())
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://test").
		Return(ctx, cancel)

	// Setup HTTP client — debug URL
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://test"}`),
	}
	httpClient.On("Do", mock.MatchedBy(matchDebugURL)).Return(resp, nil).Once()
	// Setup HTTP client — ensureCDPTargets
	targetsResp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`[{"id":"1"}]`),
	}
	httpClient.On("Do", mock.Anything).Return(targetsResp, nil).Once()
	logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

	// Create renderer
	r := &Renderer{
		logger:                logger,
		httpClient:            httpClient,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 1 * time.Millisecond,
		debugURLRetryDelay:    1 * time.Millisecond,
		debugURLMaxAttempts:   15,
		allocatorCreator:      allocatorCreator,
		containerManager:      containerMgr,
		containerName:         "headless-shell",
		debugPort:             9222,
	}
	r.resetReadyCh()
	r.setContainerReady(true)

	// Simulate existing context
	r.allocatorCtx, r.cancelAllocator = context.WithCancel(context.Background())

	// Force recovery
	r.ForceRecovery()

	assert.True(t, r.IsContainerReady())
	logger.AssertExpectations(t)
	containerMgr.AssertExpectations(t)
}

func TestWaitForChromeReady(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		logger := new(MockLogger)
		httpClient := new(MockHTTPClient)

		r := &Renderer{
			logger:             logger,
			httpClient:         httpClient,
			debugPort:          9222,
			healthCheckTimeout: 5 * time.Second,
		}

		resp := &http.Response{StatusCode: http.StatusOK}
		httpClient.On("Do", mock.Anything).Return(resp, nil).Once()

		logger.On("Info", "Waiting for Chrome health check...").Once()
		logger.On("Info", "Chrome health check passed").Once()

		err := r.waitForChromeReady()
		assert.NoError(t, err)

		httpClient.AssertExpectations(t)
		logger.AssertExpectations(t)
	})

	t.Run("Timeout", func(t *testing.T) {
		logger := new(MockLogger)
		httpClient := new(MockHTTPClient)

		r := &Renderer{
			logger:             logger,
			httpClient:         httpClient,
			debugPort:          9222,
			healthCheckTimeout: 1500 * time.Millisecond,
		}

		httpClient.On("Do", mock.Anything).Return((*http.Response)(nil), errors.New("connection refused")).Maybe()
		logger.On("Info", "Waiting for Chrome health check...").Once()

		err := r.waitForChromeReady()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "chrome health check timeout")

		logger.AssertExpectations(t)
	})
}

func TestShouldRestart(t *testing.T) {
	r := &Renderer{}

	testCases := []struct {
		name     string
		err      error
		expected bool
	}{
		// Chrome infrastructure errors → SHOULD restart
		{"WebSocketDialError", errors.New("could not dial \"ws:"), true},
		{"ChromeNotFound", errors.New("exec: \"google-chrome\": not found"), true},
		{"InvalidContext", ErrInvalidContext, true},
		{"DOMNodeNotFound", ErrDOMNodeNotFound, true},

		// Page navigation errors → should NOT restart Chrome
		{"PageConnectionRefused", errors.New("page load error net::ERR_CONNECTION_REFUSED"), false},
		{"PageTimeout", errors.New("page load error net::ERR_CONNECTION_TIMED_OUT"), false},
		{"PageNameNotResolved", errors.New("page load error net::ERR_NAME_NOT_RESOLVED"), false},
		{"PageConnectionReset", errors.New("page load error net::ERR_CONNECTION_RESET"), false},
		{"TargetSiteError", ErrTargetSiteError, false},

		// Other errors → should NOT restart
		{"OtherError", errors.New("random error"), false},
		{"TimeoutExceeded", ErrTimeoutExceeded, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := r.shouldRestart(tc.err)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestIsPageNavigationError(t *testing.T) {
	testCases := []struct {
		name     string
		err      error
		expected bool
	}{
		{"PageConnectionRefused", errors.New("page load error net::ERR_CONNECTION_REFUSED"), true},
		{"PageTimeout", errors.New("page load error net::ERR_CONNECTION_TIMED_OUT"), true},
		{"PageNameNotResolved", errors.New("page load error net::ERR_NAME_NOT_RESOLVED"), true},
		{"PageConnectionReset", errors.New("page load error net::ERR_CONNECTION_RESET"), true},
		{"PageTimedOut", errors.New("page load error net::ERR_TIMED_OUT"), true},
		{"BareConnectionRefused", errors.New("net::ERR_CONNECTION_REFUSED"), false},
		{"ChromeDialError", errors.New("could not dial \"ws:"), false},
		{"RandomError", errors.New("something else"), false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, isPageNavigationError(tc.err))
		})
	}
}

func TestGetDebugURLWithRetry(t *testing.T) {
	t.Run("SuccessOnFirstTry", func(t *testing.T) {
		logger := new(MockLogger)
		httpClient := new(MockHTTPClient)

		r := &Renderer{
			logger:              logger,
			httpClient:          httpClient,
			debugPort:           9222,
			debugURLRetryDelay:  1 * time.Millisecond,
			debugURLMaxAttempts: 3,
		}

		// Debug URL request
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       mockBody(`{"webSocketDebuggerUrl": "ws://test"}`),
		}
		httpClient.On("Do", mock.MatchedBy(matchDebugURL)).Return(resp, nil).Once()
		// ensureCDPTargets request
		targetsResp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       mockBody(`[{"id":"1"}]`),
		}
		httpClient.On("Do", mock.Anything).Return(targetsResp, nil).Once()
		logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

		url, err := r.getDebugURLWithRetry()
		assert.NoError(t, err)
		assert.Equal(t, "ws://test", url)

		httpClient.AssertExpectations(t)
	})

	t.Run("SuccessAfterRetries", func(t *testing.T) {
		logger := new(MockLogger)
		httpClient := new(MockHTTPClient)

		r := &Renderer{
			logger:              logger,
			httpClient:          httpClient,
			debugPort:           9222,
			debugURLRetryDelay:  1 * time.Millisecond,
			debugURLMaxAttempts: 3,
		}

		// First two attempts fail with connection refused
		httpClient.On("Do", mock.MatchedBy(matchDebugURL)).
			Return((*http.Response)(nil), errors.New("connection refused")).Twice()
		// Third attempt succeeds
		httpClient.On("Do", mock.MatchedBy(matchDebugURL)).
			Return(&http.Response{
				StatusCode: http.StatusOK,
				Body:       mockBody(`{"webSocketDebuggerUrl": "ws://test"}`),
			}, nil).Once()
		// ensureCDPTargets request after success
		targetsResp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       mockBody(`[{"id":"1"}]`),
		}
		httpClient.On("Do", mock.Anything).Return(targetsResp, nil).Once()
		logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

		url, err := r.getDebugURLWithRetry()
		assert.NoError(t, err)
		assert.Equal(t, "ws://test", url)

		httpClient.AssertExpectations(t)
	})

	t.Run("AllAttemptsFail", func(t *testing.T) {
		logger := new(MockLogger)
		httpClient := new(MockHTTPClient)

		r := &Renderer{
			logger:              logger,
			httpClient:          httpClient,
			debugPort:           9222,
			debugURLRetryDelay:  1 * time.Millisecond,
			debugURLMaxAttempts: 3,
		}

		httpClient.On("Do", mock.MatchedBy(matchDebugURL)).
			Return((*http.Response)(nil), errors.New("connection refused")).Times(3)
		logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

		_, err := r.getDebugURLWithRetry()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed after 3 attempts")

		httpClient.AssertExpectations(t)
	})
}

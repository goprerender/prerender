package renderer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// region Test Utilities

// matchDebugURL проверяет соответствие запроса debug URL
func matchDebugURL(req *http.Request) bool {
	return req != nil &&
		req.URL != nil &&
		req.URL.String() == "http://localhost:9222/json/version" &&
		req.Method == "GET"
}

// mockBody создает мок для тела HTTP ответа
func mockBody(content string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(content))
}

// isChanClosed проверяет закрыт ли канал
func isChanClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
	}
	return false
}

// setupHTTPMocks configures standard HTTP mocks for debug URL + CDP targets
func setupHTTPMocks(httpClient *HTTPClientMock) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://test"}`),
	}
	httpClient.On("Do", mock.MatchedBy(matchDebugURL)).Return(resp, nil).Once()
	// ensureCDPTargets call
	targetsResp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`[{"id":"1"}]`),
	}
	httpClient.On("Do", mock.Anything).Return(targetsResp, nil).Once()
}

// newTestRenderer creates a Renderer with standard test mocks
func newTestRenderer(
	logger *LoggerMock,
	httpClient *HTTPClientMock,
	allocatorCreator *AllocatorCreatorMock,
	containerMgr *ContainerManagerMock,
) *Renderer {
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
	return r
}

// endregion

// region Core Tests

func TestRendererLifecycle(t *testing.T) {
	logger := new(LoggerMock)
	httpClient := new(HTTPClientMock)
	allocatorCreator := new(AllocatorCreatorMock)
	containerMgr := new(ContainerManagerMock)

	// Logger expectations
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()
	logger.On("Info", "Connecting to Chrome...").Once()
	logger.On("Infof", "Using Chrome debug URL: %s", "ws://test").Once()
	logger.On("Info", "Connected to Chrome via remote allocator").Once()
	logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

	containerMgr.On("EnsureRunning").Return(nil).Once()
	setupHTTPMocks(httpClient)

	ctx, cancel := context.WithCancel(context.Background())
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://test").
		Return(ctx, cancel)

	r := newTestRenderer(logger, httpClient, allocatorCreator, containerMgr)
	r.Setup()
	defer r.Cancel()

	assert.True(t, r.IsContainerReady())

	logger.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	allocatorCreator.AssertExpectations(t)
	containerMgr.AssertExpectations(t)
}

func TestContainerStartFailure(t *testing.T) {
	logger := new(LoggerMock)
	httpClient := new(HTTPClientMock)
	allocatorCreator := new(AllocatorCreatorMock)
	containerMgr := new(ContainerManagerMock)

	// Logger expectations
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()
	logger.On("Errorf", "Container setup error: %v", mock.Anything).Once()
	logger.On("Info", "Connecting to Chrome...").Once()
	logger.On("Error", "Failed to connect to Chrome container").Once()
	logger.On("Errorf", "Connection error: %v", mock.Anything).Once()
	logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

	containerMgr.On("EnsureRunning").Return(errors.New("docker not found")).Once()

	// HTTP client will fail on all attempts
	httpClient.On("Do", mock.Anything).Return((*http.Response)(nil), errors.New("connection refused")).Times(3)

	r := newTestRenderer(logger, httpClient, allocatorCreator, containerMgr)
	r.debugURLMaxAttempts = 3
	r.Setup()

	assert.False(t, r.IsContainerReady())

	logger.AssertExpectations(t)
	containerMgr.AssertExpectations(t)
}

func TestSuccessfulRender(t *testing.T) {
	logger := new(LoggerMock)
	httpClient := new(HTTPClientMock)
	allocatorCreator := new(AllocatorCreatorMock)
	containerMgr := new(ContainerManagerMock)
	pageRenderer := new(PageRendererMock)

	// Logger expectations
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()
	logger.On("Info", "Connecting to Chrome...").Once()
	logger.On("Infof", "Using Chrome debug URL: %s", "ws://test").Once()
	logger.On("Info", "Connected to Chrome via remote allocator").Once()
	logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

	containerMgr.On("EnsureRunning").Return(nil).Once()
	setupHTTPMocks(httpClient)

	ctx, cancel := context.WithCancel(context.Background())
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://test").
		Return(ctx, cancel)

	r := newTestRenderer(logger, httpClient, allocatorCreator, containerMgr)
	r.SetPageRenderer(pageRenderer)
	r.Setup()
	r.setContainerReady(true)

	pageRenderer.On("RenderPage", "https://example.com", mock.Anything).
		Return("<html>Test Content</html>", nil).Once()

	result, err := r.DoRender("https://example.com")
	assert.NoError(t, err)
	assert.Equal(t, "<html>Test Content</html>", result.HTML)
	assert.GreaterOrEqual(t, result.TotalTime, time.Duration(0))

	r.Cancel()

	logger.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	allocatorCreator.AssertExpectations(t)
	containerMgr.AssertExpectations(t)
	pageRenderer.AssertExpectations(t)
}

func TestRenderWithRestart(t *testing.T) {
	logger := new(LoggerMock)
	httpClient := new(HTTPClientMock)
	allocatorCreator := new(AllocatorCreatorMock)
	containerMgr := new(ContainerManagerMock)
	pageRenderer := new(PageRendererMock)

	// Logger expectations — allow all log calls flexibly
	logger.On("Info", mock.Anything).Maybe()
	logger.On("Infof", mock.Anything, mock.Anything).Maybe()
	logger.On("Warn", mock.Anything).Maybe()
	logger.On("Warnf", mock.Anything, mock.Anything).Maybe()
	logger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Maybe()
	logger.On("Warnf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe()
	logger.On("Error", mock.Anything).Maybe()
	logger.On("Errorf", mock.Anything, mock.Anything).Maybe()
	logger.On("Errorf", mock.Anything, mock.Anything, mock.Anything).Maybe()
	logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

	// Container manager: status before restart, restart, status after restart
	containerMgr.On("GetStatus").Return("exited").Once()
	containerMgr.On("Restart").Return(nil).Once()
	containerMgr.On("GetStatus").Return("running").Once()

	// Allocator for initial + after restart
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://test").
		Return(ctx1, cancel1).Once()
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://new").
		Return(ctx2, cancel2).Once()

	// HTTP mocks for restart flow:
	// 1. waitForChromeReady → GET /json/version (health check, only checks status)
	// 2. getDebugURL → GET /json/version (reads body for webSocketDebuggerUrl)
	// 3. ensureCDPTargets → GET /json (reads body for targets)
	matchVersionURL := func(req *http.Request) bool {
		return req != nil && req.URL != nil &&
			strings.HasSuffix(req.URL.Path, "/json/version") && req.Method == "GET"
	}
	// Health check response (waitForChromeReady reads only status)
	httpClient.On("Do", mock.MatchedBy(matchVersionURL)).Return(&http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://new"}`),
	}, nil).Once()
	// Debug URL response (getDebugURL reads body)
	httpClient.On("Do", mock.MatchedBy(matchVersionURL)).Return(&http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://new"}`),
	}, nil).Once()
	// CDP targets response
	httpClient.On("Do", mock.Anything).Return(&http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`[{"id":"1"}]`),
	}, nil).Maybe()

	r := &Renderer{
		logger:                logger,
		httpClient:            httpClient,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 30 * time.Second,
		debugURLRetryDelay:    1 * time.Millisecond,
		debugURLMaxAttempts:   20,
		allocatorCreator:      allocatorCreator,
		containerManager:      containerMgr,
		containerName:         "headless-shell",
		debugPort:             9222,
		renderTimeout:         30 * time.Second,
	}
	r.resetReadyCh()
	r.isStarted = true
	r.setRemoteAllocator("ws://test")
	r.setContainerReady(true)
	r.SetPageRenderer(pageRenderer)

	// First attempt fails with ws error (triggers restart)
	pageRenderer.On("RenderPage", "https://example.com", mock.Anything).
		Return("", errors.New("could not dial \"ws:")).Once()
	// Second attempt succeeds
	pageRenderer.On("RenderPage", "https://example.com", mock.Anything).
		Return("<html>Restarted</html>", nil).Once()

	result, err := r.DoRender("https://example.com")
	assert.NoError(t, err)
	assert.Equal(t, "<html>Restarted</html>", result.HTML)

	pageRenderer.AssertExpectations(t)
	containerMgr.AssertExpectations(t)
}

func TestConcurrentRendering(t *testing.T) {
	logger := new(LoggerMock)
	httpClient := new(HTTPClientMock)
	allocatorCreator := new(AllocatorCreatorMock)
	containerMgr := new(ContainerManagerMock)
	pageRenderer := new(PageRendererMock)

	logger.On("Info", mock.Anything).Maybe()
	logger.On("Infof", mock.Anything, mock.Anything).Maybe()
	logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

	containerMgr.On("EnsureRunning").Return(nil).Once()
	setupHTTPMocks(httpClient)

	ctx, cancel := context.WithCancel(context.Background())
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, mock.Anything).
		Return(ctx, cancel)

	r := newTestRenderer(logger, httpClient, allocatorCreator, containerMgr)
	r.setContainerReady(true)
	r.SetPageRenderer(pageRenderer)
	r.Setup()
	defer r.Cancel()

	for i := 0; i < 5; i++ {
		pageRenderer.On("RenderPage", fmt.Sprintf("https://example.com/page/%d", i), mock.Anything).
			Return(fmt.Sprintf("<html>Page %d</html>", i), nil).Once()
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			url := fmt.Sprintf("https://example.com/page/%d", id)
			result, err := r.DoRender(url)
			assert.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("<html>Page %d</html>", id), result.HTML)
		}(i)
	}
	wg.Wait()

	assert.Equal(t, 0, len(r.semaphore))
	pageRenderer.AssertExpectations(t)
}

func TestContextCancellation(t *testing.T) {
	logger := new(LoggerMock)
	httpClient := new(HTTPClientMock)
	allocatorCreator := new(AllocatorCreatorMock)
	containerMgr := new(ContainerManagerMock)
	pageRenderer := new(PageRendererMock)

	logger.On("Info", mock.Anything).Maybe()
	logger.On("Infof", mock.Anything, mock.Anything).Maybe()
	logger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Maybe()
	logger.On("Debugf", mock.Anything, mock.Anything).Maybe()

	containerMgr.On("EnsureRunning").Return(nil).Once()
	setupHTTPMocks(httpClient)

	ctx, cancel := context.WithCancel(context.Background())
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://test").
		Return(ctx, cancel)

	r := newTestRenderer(logger, httpClient, allocatorCreator, containerMgr)
	r.SetPageRenderer(pageRenderer)
	r.Setup()
	r.setContainerReady(true)

	pageRenderer.On("RenderPage", "https://example.com", mock.Anything).
		Return("", context.Canceled).Once()

	r.Cancel()

	result, err := r.DoRender("https://example.com")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, errors.Is(err, ErrContextCanceled) ||
		strings.Contains(err.Error(), "context canceled"))

	pageRenderer.AssertExpectations(t)
}

func TestContainerReadyStateManagement(t *testing.T) {
	r := &Renderer{}
	r.resetReadyCh()

	assert.False(t, r.isContainerReady())

	r.setContainerReady(true)
	assert.True(t, r.isContainerReady())
	assert.True(t, isChanClosed(r.readyCh))

	r.setContainerReady(false)
	assert.False(t, r.isContainerReady())
	assert.False(t, isChanClosed(r.readyCh))
}

// endregion

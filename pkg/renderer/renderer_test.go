package renderer

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"golang.org/x/net/context"
)

// MockAllocatorCreator для тестов
type MockAllocatorCreator struct {
	mock.Mock
}

func (m *MockAllocatorCreator) CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc) {
	m.Called(ctx, url)
	return context.Background(), func() {}
}

// matchDebugURL проверяет соответствие запроса ожидаемым параметрам
func matchDebugURL(req *http.Request) bool {
	return req != nil &&
		req.URL != nil &&
		req.URL.String() == "http://localhost:9222/json/version" &&
		req.Method == "GET"
}

// mockBody создает мок для тела HTTP-ответа
func mockBody(content string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(content))
}

// MockLogger мок для логгера
type MockLogger struct {
	mock.Mock
}

func (m *MockLogger) Info(args ...interface{}) {
	m.Called(args...)
}

func (m *MockLogger) Infof(format string, args ...interface{}) {
	m.Called(append([]interface{}{format}, args...)...)
}

func (m *MockLogger) Warn(args ...interface{}) {
	m.Called(args...)
}

func (m *MockLogger) Warnf(format string, args ...interface{}) {
	m.Called(append([]interface{}{format}, args...)...)
}

func (m *MockLogger) Error(args ...interface{}) {
	m.Called(args...)
}

func (m *MockLogger) Errorf(format string, args ...interface{}) {
	m.Called(append([]interface{}{format}, args...)...)
}

func (m *MockLogger) Debug(args ...interface{}) {
	m.Called(args...)
}

func (m *MockLogger) Debugf(format string, args ...interface{}) {
	m.Called(append([]interface{}{format}, args...)...)
}

// MockCommander мок для команд
type MockCommander struct {
	mock.Mock
}

func (m *MockCommander) LookPath(file string) (string, error) {
	args := m.Called(file)
	return args.String(0), args.Error(1)
}

func (m *MockCommander) Command(name string, arg ...string) *exec.Cmd {
	args := m.Called(name, arg)
	return args.Get(0).(*exec.Cmd)
}

// MockHTTPClient мок для HTTP-клиента
type MockHTTPClient struct {
	mock.Mock
}

func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	args := m.Called(req)
	return args.Get(0).(*http.Response), args.Error(1)
}

// MockPortChecker мок для проверки портов
type MockPortChecker struct {
	mock.Mock
}

func (m *MockPortChecker) IsPortAvailable(port int) bool {
	args := m.Called(port)
	return args.Bool(0)
}

// MockSleeper мок для задержек
type MockSleeper struct {
	mock.Mock
}

func (m *MockSleeper) Sleep(d time.Duration) {
	m.Called(d)
}

// MockPageRenderer мок для рендеринга страниц
type MockPageRenderer struct {
	mock.Mock
}

func (m *MockPageRenderer) RenderPage(url string, result *RenderResult) (string, error) {
	args := m.Called(url, result)
	return args.String(0), args.Error(1)
}

func TestRendererLifecycle(t *testing.T) {
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	portChecker := new(MockPortChecker)
	allocatorCreator := new(MockAllocatorCreator)

	// Setup expectations
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()
	logger.On("Infof", "Initial container status: %s", "running").Once()
	logger.On("Info", "Container setup completed").Once()
	logger.On("Info", "Connecting to Chrome...").Once()
	logger.On("Infof", "Using Chrome debug URL: %s", "ws://test").Once()
	logger.On("Info", "Connected to Chrome via remote allocator").Once()

	// Setup allocator creator
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://test").Return(context.Background(), func() {})

	// Setup commander
	commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()
	cmdStatus := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus).Once()

	// Setup HTTP client
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://test"}`),
	}
	httpClient.On("Do", mock.MatchedBy(matchDebugURL)).Return(resp, nil).Once()

	// Setup port checker
	portChecker.On("IsPortAvailable", 9222).Return(true).Once()

	// Create renderer
	r := &Renderer{
		logger:                logger,
		commander:             commander,
		httpClient:            httpClient,
		portChecker:           portChecker,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 1 * time.Millisecond,
		containerStartDelay:   1 * time.Millisecond,
		debugURLRetryDelay:    1 * time.Millisecond,
		debugURLMaxAttempts:   15,
		allocatorCreator:      allocatorCreator,
		blockedURLs: []string{
			"google-analytics.com",
			"mc.yandex.ru",
			"maps.googleapis.com",
			"googletagmanager.com",
			"api-maps.yandex.ru",
			"doubleclick.net",
			"facebook.net",
		},
	}
	r.resetReadyCh()

	r.Setup()
	defer r.Cancel()

	assert.True(t, r.isContainerReady())

	logger.AssertExpectations(t)
	commander.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	portChecker.AssertExpectations(t)
	allocatorCreator.AssertExpectations(t)
}

func TestContainerStartFailure(t *testing.T) {
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	portChecker := new(MockPortChecker)
	allocatorCreator := new(MockAllocatorCreator)

	// Setup expectations
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()
	logger.On("Error", "Docker not found").Once()
	logger.On("Info", "Connecting to Chrome...").Once()
	logger.On("Debugf", "Debug URL attempt failed (%d/%d): %v", mock.Anything, mock.Anything, mock.Anything).Times(3)
	logger.On("Error", "Failed to connect to Chrome container").Once()
	logger.On("Errorf", "Connection error: %v", mock.Anything).Once()
	logger.On("Errorf", "Container setup error: %v", mock.Anything).Once()

	commander.On("LookPath", "docker").Return("", errors.New("not found")).Once()
	portChecker.On("IsPortAvailable", 9222).Return(false).Times(3)

	// Create renderer
	r := &Renderer{
		logger:                logger,
		commander:             commander,
		httpClient:            httpClient,
		portChecker:           portChecker,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 1 * time.Millisecond,
		containerStartDelay:   1 * time.Millisecond,
		debugURLRetryDelay:    1 * time.Millisecond,
		debugURLMaxAttempts:   3,
		allocatorCreator:      allocatorCreator,
		blockedURLs: []string{
			"google-analytics.com",
			"mc.yandex.ru",
			"maps.googleapis.com",
			"googletagmanager.com",
			"api-maps.yandex.ru",
			"doubleclick.net",
			"facebook.net",
		},
	}
	r.resetReadyCh()

	r.Setup()

	assert.False(t, r.isContainerReady())
	httpClient.AssertNotCalled(t, "Do", mock.Anything)

	logger.AssertExpectations(t)
	commander.AssertExpectations(t)
	portChecker.AssertExpectations(t)
}

func TestSuccessfulRender(t *testing.T) {
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	portChecker := new(MockPortChecker)
	allocatorCreator := new(MockAllocatorCreator)
	pageRenderer := new(MockPageRenderer)

	// Setup logger expectations
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()
	logger.On("Infof", "Initial container status: %s", "running").Once()
	logger.On("Info", "Container setup completed").Once()
	logger.On("Info", "Connecting to Chrome...").Once()
	logger.On("Infof", "Using Chrome debug URL: %s", "ws://test").Once()
	logger.On("Info", "Connected to Chrome via remote allocator").Once()

	// Setup allocator creator
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://test").Return(context.Background(), func() {})

	// Setup commander expectations
	commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()
	cmdStatus := exec.Command("echo", "running")
	// ИСПРАВЛЕНИЕ: Ожидаем только один вызов
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus).Once()

	// Setup HTTP client expectations
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://test"}`),
	}
	httpClient.On("Do", mock.MatchedBy(matchDebugURL)).Return(resp, nil).Once()

	// Setup port checker
	portChecker.On("IsPortAvailable", 9222).Return(true).Once()

	// Create renderer
	r := &Renderer{
		logger:                logger,
		commander:             commander,
		httpClient:            httpClient,
		portChecker:           portChecker,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 1 * time.Millisecond,
		containerStartDelay:   1 * time.Millisecond,
		debugURLRetryDelay:    1 * time.Millisecond,
		debugURLMaxAttempts:   15,
		allocatorCreator:      allocatorCreator,
		blockedURLs: []string{
			"google-analytics.com",
			"mc.yandex.ru",
			"maps.googleapis.com",
			"googletagmanager.com",
			"api-maps.yandex.ru",
			"doubleclick.net",
			"facebook.net",
		},
	}
	r.resetReadyCh()

	r.Setup()
	r.setContainerReady(true)
	r.SetPageRenderer(pageRenderer)

	// Setup page renderer expectations
	pageRenderer.On("RenderPage", "https://example.com", mock.Anything).
		Return("<html>Test Content</html>", nil).
		Once()

	// Attempt render
	result, err := r.DoRender("https://example.com")

	// Verify results
	assert.NoError(t, err)
	assert.Equal(t, "<html>Test Content</html>", result.HTML)
	assert.GreaterOrEqual(t, result.TotalTime, time.Duration(0))
	assert.GreaterOrEqual(t, result.RenderTime, time.Duration(0))

	// Cleanup
	r.Cancel()

	// Verify all expectations
	logger.AssertExpectations(t)
	commander.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	portChecker.AssertExpectations(t)
	allocatorCreator.AssertExpectations(t)
	pageRenderer.AssertExpectations(t)
}

func TestRenderWithRestart(t *testing.T) {
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	allocatorCreator := new(MockAllocatorCreator)
	pageRenderer := new(MockPageRenderer)

	// Настраиваем ожидания только для операций перезапуска
	logger.On("Errorf", "Render attempt failed (attempt %d): %v", 1, mock.Anything).Once()
	logger.On("Warn", "Initiating container restart...").Once()
	logger.On("Info", "Waiting for active requests to complete before restart...").Once()
	logger.On("Info", "All active requests completed").Once()
	logger.On("Info", "Restarting container...").Once()
	logger.On("Warnf", "Container status: %s, restarting...", "exited").Once()
	// Убрали ожидание лога "Using Chrome debug URL: ws://new" - его нет в реальном коде
	logger.On("Info", "Container restarted successfully").Once() // Оставили только этот лог

	// Настройка аллокатора
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://test").Return(context.Background(), func() {})
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://new").Return(context.Background(), func() {})

	// Команды для Docker
	cmdExited := exec.Command("echo", "exited")
	cmdRunning := exec.Command("echo", "running")
	cmdRestart := exec.Command("true")

	// Ожидаемая последовательность вызовов Docker
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdExited).Once()
	commander.On("Command", "docker", []string{"restart", containerName}).Return(cmdRestart).Once()
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdRunning).Once()

	// HTTP-ответ для получения нового debug URL
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://new"}`),
	}
	httpClient.On("Do", mock.MatchedBy(matchDebugURL)).Return(resp, nil).Once()

	// Создаем рендерер с ручной инициализацией состояния
	r := NewRenderer(logger, commander, httpClient)
	r.allocatorCreator = allocatorCreator
	r.SetPageRenderer(pageRenderer)
	r.sleeper = func(d time.Duration) {}
	r.dockerPath = "docker"
	r.isStarted = true
	r.setRemoteAllocator("ws://test")
	r.setContainerReady(true)

	// Настраиваем поведение рендерера страниц
	pageRenderer.On("RenderPage", "https://example.com", mock.Anything).
		Return("", errors.New("could not dial \"ws:")). // Ошибка для триггера перезапуска
		Once()

	pageRenderer.On("RenderPage", "https://example.com", mock.Anything).
		Return("<html>Restarted</html>", nil). // Успешный рендер после перезапуска
		Once()

	// Выполняем рендеринг
	result, err := r.DoRender("https://example.com")
	assert.NoError(t, err)
	assert.Equal(t, "<html>Restarted</html>", result.HTML)

	// Проверяем все ожидания
	logger.AssertExpectations(t)
	commander.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	allocatorCreator.AssertExpectations(t)
	pageRenderer.AssertExpectations(t)
}

func TestConcurrentRendering(t *testing.T) {
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	portChecker := new(MockPortChecker)
	allocatorCreator := new(MockAllocatorCreator)
	pageRenderer := new(MockPageRenderer)

	// Setup minimal expectations for initialization
	logger.On("Info", mock.Anything).Times(5)
	logger.On("Infof", mock.Anything, mock.Anything).Times(3)

	// Setup allocator creator
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, mock.Anything).Return(context.Background(), func() {})

	commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()
	cmdStatus := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus).Twice()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://test"}`),
	}
	httpClient.On("Do", mock.MatchedBy(matchDebugURL)).Return(resp, nil).Once()
	portChecker.On("IsPortAvailable", 9222).Return(true).Once()

	// Create renderer
	r := &Renderer{
		logger:                logger,
		commander:             commander,
		httpClient:            httpClient,
		portChecker:           portChecker,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 1 * time.Millisecond,
		containerStartDelay:   1 * time.Millisecond,
		debugURLRetryDelay:    1 * time.Millisecond,
		debugURLMaxAttempts:   15,
		allocatorCreator:      allocatorCreator,
	}
	r.resetReadyCh()
	r.setContainerReady(true)
	r.SetPageRenderer(pageRenderer)
	r.Setup()
	defer r.Cancel()

	// Setup page renderer expectations
	for i := 0; i < 5; i++ {
		pageRenderer.On("RenderPage", fmt.Sprintf("https://example.com/page/%d", i), mock.Anything).
			Return(fmt.Sprintf("<html>Page %d</html>", i), nil).
			Once()
	}

	// Simulate concurrent renders
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			url := fmt.Sprintf("https://example.com/page/%d", id)
			result, err := r.DoRender(url)
			assert.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("<html>Page %d</html>", id), result.HTML)
		}(i) // Фиксируем значение id
	}
	wg.Wait()

	// Verify semaphore released all slots
	assert.Equal(t, 0, len(r.semaphore)) // Все слоты должны освободиться
	pageRenderer.AssertExpectations(t)
}

func TestContextCancellation(t *testing.T) {
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	portChecker := new(MockPortChecker)
	allocatorCreator := new(MockAllocatorCreator)
	pageRenderer := new(MockPageRenderer)

	// Setup expectations
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()
	logger.On("Infof", "Initial container status: %s", "running").Once()
	logger.On("Info", "Container setup completed").Once()
	logger.On("Info", "Connecting to Chrome...").Once()
	logger.On("Infof", "Using Chrome debug URL: %s", "ws://test").Once()
	logger.On("Info", "Connected to Chrome via remote allocator").Once()
	logger.On("Warnf", "Render canceled for %s: %v", "https://example.com", mock.Anything).Once()

	// Setup allocator creator
	allocatorCreator.On("CreateRemoteAllocator", mock.Anything, "ws://test").Return(context.Background(), func() {})

	commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()
	cmdStatus := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus).Once()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://test"}`),
	}
	httpClient.On("Do", mock.MatchedBy(matchDebugURL)).Return(resp, nil).Once()
	portChecker.On("IsPortAvailable", 9222).Return(true).Once()

	r := NewRenderer(logger, commander, httpClient)
	r.portChecker = portChecker
	r.allocatorCreator = allocatorCreator
	r.SetPageRenderer(pageRenderer)
	r.Setup()
	defer r.Cancel()

	// Set container ready
	r.setContainerReady(true)

	// Устанавливаем ожидание для рендерера страниц
	pageRenderer.On("RenderPage", "https://example.com", mock.Anything).
		Return("", context.Canceled). // Возвращаем ошибку отмены
		Once()

	// Immediately cancel context before rendering
	r.Cancel()

	// Attempt render should fail with context canceled
	result, err := r.DoRender("https://example.com")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, errors.Is(err, ErrContextCanceled) ||
		strings.Contains(err.Error(), "context canceled"))

	logger.AssertExpectations(t)
	commander.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	portChecker.AssertExpectations(t)
	allocatorCreator.AssertExpectations(t)
	pageRenderer.AssertExpectations(t)
}

func TestContainerReadyStateManagement(t *testing.T) {
	r := &Renderer{}
	r.resetReadyCh()

	// Initial state should be not ready
	assert.False(t, r.isContainerReady())

	// Set to ready
	r.setContainerReady(true)
	assert.True(t, r.isContainerReady())
	assert.True(t, isChanClosed(r.readyCh))

	// Set back to not ready
	r.setContainerReady(false)
	assert.False(t, r.isContainerReady())
	assert.False(t, isChanClosed(r.readyCh))
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

// Закомментировано на время, до фиксации всех остальных тестов. Не стирай, пожалуйста.
/*func TestRemoteAllocatorLifecycle(t *testing.T) {
	// Используем реальный allocator вместо мока
	allocator := &renderer.RealAllocatorCreator{}

	// Создаем аллокатор с реальным соединением
	ctx, cancel := allocator.CreateRemoteAllocator(context.Background(), "ws://localhost:9222/devtools/browser/...")
	defer cancel()

	// Проверяем, что контекст валиден
	select {
	case <-ctx.Done():
		t.Fatal("Context canceled unexpectedly")
	default:
	}

	// Имитируем работу с контекстом
	time.Sleep(2 * time.Second)

	// Проверяем, что контекст все еще валиден
	select {
	case <-ctx.Done():
		t.Fatal("Context should not be canceled")
	default:
	}
}*/

// Закомментировано на время, до фиксации всех остальных тестов. Не стирай, пожалуйста.
/*func TestContextRecreation(t *testing.T) {
	r := createTestRenderer()
	originalCtx := r.allocatorCtx

	r.setRemoteAllocator("ws://new-url")

	if r.allocatorCtx == originalCtx {
		t.Error("Allocator context should be recreated")
	}

	if originalCtx.Err() == nil {
		t.Error("Original context should be canceled")
	}
}*/

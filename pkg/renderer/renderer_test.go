package renderer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

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

func (m *MockHTTPClient) Get(url string) (*http.Response, error) {
	args := m.Called(url)
	return args.Get(0).(*http.Response), args.Error(1)
}

// mockBody создает мок для тела HTTP-ответа
func mockBody(content string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(content))
}

func TestSanity(t *testing.T) {
	assert.True(t, true, "Тестовая среда работает")
}

// MockPortChecker мок для проверки портов
type MockPortChecker struct {
	mock.Mock
}

func (m *MockPortChecker) IsPortAvailable(port int) bool {
	args := m.Called(port)
	return args.Bool(0)
}

func TestSetContainerReady(t *testing.T) {
	t.Log("Starting TestSetContainerReady")
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	portChecker := new(MockPortChecker)

	// Настраиваем ожидания для вызовов в Setup()
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()

	// Мокируем Docker
	commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()

	// Первый вызов getContainerStatus
	t.Log("Mocking first container status check")
	cmdStatus1 := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus1).Once()
	logger.On("Infof", "Initial container status: %s", "running").Once()

	// Второй вызов getContainerStatus (после проверки)
	t.Log("Mocking second container status check")
	cmdStatus2 := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus2).Once()

	logger.On("Info", "Container setup completed").Once()
	logger.On("Info", "Connecting to Chrome...").Once()

	// Мокируем HTTP-запрос
	t.Log("Mocking HTTP request to debugURL")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://localhost:9222/devtools/browser/test"}`),
	}
	httpClient.On("Get", debugURL).Return(resp, nil).Once()

	// Мокируем проверку порта
	portChecker.On("IsPortAvailable", 9222).Return(true).Once()

	// Добавляем ожидания для вызовов при успешном подключении
	logger.On("Infof", "Using Chrome debug URL: %s", "ws://localhost:9222/devtools/browser/test").Once()
	logger.On("Info", "Connected to Chrome via remote allocator").Once()

	t.Log("Creating NewRenderer")
	r := &Renderer{
		logger:                logger,
		commander:             commander,
		httpClient:            httpClient,
		portChecker:           portChecker,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 60 * time.Second,
		containerStartDelay:   10 * time.Second,
		debugURLRetryDelay:    1 * time.Second,
		debugURLMaxAttempts:   15,
	}
	r.resetReadyCh()
	r.Setup()
	t.Log("NewRenderer created")

	// Проверяем состояние после Setup
	t.Log("Checking container state after Setup")
	assert.True(t, r.isContainerReady()) // После Setup контейнер должен быть готов

	// Сбрасываем готовность
	t.Log("Setting container not ready")
	r.setContainerReady(false)
	assert.False(t, r.isContainerReady())

	// Устанавливаем готовность
	t.Log("Setting container ready")
	r.setContainerReady(true)
	assert.True(t, r.isContainerReady())

	t.Log("Verifying mock expectations")
	logger.AssertExpectations(t)
	commander.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	portChecker.AssertExpectations(t)
	t.Log("All mock expectations verified")
}

func TestPortNotAvailable(t *testing.T) {
	t.Log("Starting TestPortNotAvailable")
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	portChecker := new(MockPortChecker)
	sleeper := new(MockSleeper)

	// Ожидаемые вызовы при инициализации
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()

	// Мокируем Docker
	commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()

	// Создаем команды, которые возвращают статус "running"
	cmdStatus1 := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus1).Once()
	logger.On("Infof", "Initial container status: %s", "running").Once()

	cmdStatus2 := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus2).Once()

	logger.On("Info", "Container setup completed").Once()
	logger.On("Info", "Connecting to Chrome...").Once()

	// Мокируем проверку порта
	portChecker.On("IsPortAvailable", 9222).Return(false).Times(3)
	logger.On("Debugf", "Debug URL attempt failed (%d/%d): %v", mock.Anything, mock.Anything, mock.Anything).Times(3)
	logger.On("Error", "Failed to connect to Chrome container").Once()
	logger.On("Errorf", "Connection error: %v", mock.Anything).Once()

	// Мокируем задержки
	sleeper.On("Sleep", mock.Anything).Times(2)

	t.Log("Creating NewRenderer when port is not available")
	r := &Renderer{
		logger:                logger,
		commander:             commander,
		httpClient:            httpClient,
		portChecker:           portChecker,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 500 * time.Millisecond,
		containerStartDelay:   0,
		debugURLRetryDelay:    1 * time.Millisecond,
		debugURLMaxAttempts:   3,
		sleeper:               sleeper.Sleep,
	}
	r.resetReadyCh()

	r.Setup()
	t.Log("NewRenderer created")

	// Проверяем состояние контейнера
	t.Log("Checking container state")
	assert.False(t, r.isContainerReady())

	t.Log("Verifying mock expectations")
	logger.AssertExpectations(t)
	commander.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	portChecker.AssertExpectations(t)
	sleeper.AssertExpectations(t)
	t.Log("All mock expectations verified")
}

func TestWaitForContainerReady(t *testing.T) {
	t.Log("Starting TestWaitForContainerReady")
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	portChecker := new(MockPortChecker)

	// Настраиваем ожидания для вызовов в Setup()
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()
	commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()

	// Первый вызов getContainerStatus
	cmdStatus1 := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus1).Once()
	logger.On("Infof", "Initial container status: %s", "running").Once()

	// Второй вызов getContainerStatus
	cmdStatus2 := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus2).Once()

	logger.On("Info", "Container setup completed").Once()
	logger.On("Info", "Connecting to Chrome...").Once()

	// Мокируем HTTP-запрос
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://localhost:9222/devtools/browser/test"}`),
	}
	httpClient.On("Get", debugURL).Return(resp, nil).Once()

	// Мокируем проверку порта
	portChecker.On("IsPortAvailable", 9222).Return(true).Once()

	logger.On("Infof", "Using Chrome debug URL: %s", "ws://localhost:9222/devtools/browser/test").Once()
	logger.On("Info", "Connected to Chrome via remote allocator").Once()

	// Мокируем вызовы в waitForContainerReady
	logger.On("Warnf", "Container not ready, waiting %v...", mock.Anything).Maybe()

	// Создаем рендерер
	r := &Renderer{
		logger:                logger,
		commander:             commander,
		httpClient:            httpClient,
		portChecker:           portChecker,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 60 * time.Second,
		containerStartDelay:   0,
		debugURLRetryDelay:    1 * time.Second,
		debugURLMaxAttempts:   15,
	}
	r.resetReadyCh()
	r.Setup()

	t.Run("ContainerReadyImmediately", func(t *testing.T) {
		err := r.waitForContainerReady()
		assert.NoError(t, err)
	})

	t.Run("ContainerBecomesReady", func(t *testing.T) {
		r.setContainerReady(false)
		assert.False(t, r.isContainerReady())

		go func() {
			time.Sleep(100 * time.Millisecond)
			r.setContainerReady(true)
		}()

		err := r.waitForContainerReady()
		assert.NoError(t, err)
	})

	t.Run("TimeoutWaitingForContainer", func(t *testing.T) {
		r.setContainerReady(false)
		assert.False(t, r.isContainerReady())

		originalTimeout := r.containerReadyTimeout
		r.containerReadyTimeout = 100 * time.Millisecond
		defer func() { r.containerReadyTimeout = originalTimeout }()

		err := r.waitForContainerReady()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "timeout after")
	})

	// Проверяем, что все ожидаемые вызовы были сделаны
	logger.AssertExpectations(t)
	commander.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	portChecker.AssertExpectations(t)
}

func TestShouldRestart(t *testing.T) {
	t.Log("Starting TestShouldRestart")
	logger := new(MockLogger)
	commander := new(MockCommander)
	httpClient := new(MockHTTPClient)
	portChecker := new(MockPortChecker)

	// Настраиваем ожидания для вызовов в Setup()
	logger.On("Info", "Initializing renderer...").Once()
	logger.On("Info", "Setting up container...").Once()
	commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()

	// Первый вызов getContainerStatus
	cmdStatus1 := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus1).Once()
	logger.On("Infof", "Initial container status: %s", "running").Once()

	// Второй вызов getContainerStatus
	cmdStatus2 := exec.Command("echo", "running")
	commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus2).Once()

	logger.On("Info", "Container setup completed").Once()
	logger.On("Info", "Connecting to Chrome...").Once()

	// Мокируем HTTP-запрос
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       mockBody(`{"webSocketDebuggerUrl": "ws://localhost:9222/devtools/browser/test"}`),
	}
	httpClient.On("Get", debugURL).Return(resp, nil).Once()

	// Мокируем проверку порта
	portChecker.On("IsPortAvailable", 9222).Return(true).Once()

	logger.On("Infof", "Using Chrome debug URL: %s", "ws://localhost:9222/devtools/browser/test").Once()
	logger.On("Info", "Connected to Chrome via remote allocator").Once()

	// Создаем рендерер
	r := &Renderer{
		logger:                logger,
		commander:             commander,
		httpClient:            httpClient,
		portChecker:           portChecker,
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		containerReadyTimeout: 60 * time.Second,
		containerStartDelay:   0,
		debugURLRetryDelay:    1 * time.Second,
		debugURLMaxAttempts:   15,
	}
	r.resetReadyCh()
	r.Setup()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"WsDialError", errors.New("could not dial \"ws:"), true},
		{"ChromeNotFound", errors.New("exec: \"google-chrome\":"), true},
		{"InvalidContext", ErrInvalidContext, true},
		{"NameNotResolved", ErrNameNotResolved, false},
		{"OtherError", errors.New("some other error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := r.shouldRestart(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}

	// Проверяем, что все ожидаемые вызовы были сделаны
	logger.AssertExpectations(t)
	commander.AssertExpectations(t)
	httpClient.AssertExpectations(t)
	portChecker.AssertExpectations(t)
}

func TestSetupContainer(t *testing.T) {
	t.Run("ContainerAlreadyRunning", func(t *testing.T) {
		t.Log("Starting TestSetupContainer (ContainerAlreadyRunning)")
		logger := new(MockLogger)
		commander := new(MockCommander)
		httpClient := new(MockHTTPClient)

		// Настраиваем ожидания для вызовов в Setup()
		logger.On("Info", "Initializing renderer...").Once()
		logger.On("Info", "Setting up container...").Once()

		// Мокируем Docker
		commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()

		// Вызов getContainerStatus
		cmdStatus := exec.Command("echo", "running")
		commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus).Once()
		logger.On("Infof", "Initial container status: %s", "running").Once()

		logger.On("Info", "Container setup completed").Once()
		logger.On("Info", "Connecting to Chrome...").Once()

		// Мокируем HTTP-запрос
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       mockBody(`{"webSocketDebuggerUrl": "ws://localhost:9222/devtools/browser/test"}`),
		}
		httpClient.On("Get", debugURL).Return(resp, nil).Once()

		logger.On("Info", "Using Chrome debug URL: ws://localhost:9222/devtools/browser/test").Once()
		logger.On("Info", "Connected to Chrome via remote allocator").Once()

		r := NewRenderer(logger, commander, httpClient)
		err := r.setupContainer()
		assert.NoError(t, err)
		assert.True(t, r.isStarted)

		// Проверяем, что все ожидаемые вызовы были сделаны
		logger.AssertExpectations(t)
		commander.AssertExpectations(t)
		httpClient.AssertExpectations(t)
	})

	t.Run("DockerNotFound", func(t *testing.T) {
		t.Log("Starting TestSetupContainer (DockerNotFound)")
		logger := new(MockLogger)
		commander := new(MockCommander)
		httpClient := new(MockHTTPClient)

		// Настраиваем ожидания для вызовов в Setup()
		logger.On("Info", "Initializing renderer...").Once()
		logger.On("Info", "Setting up container...").Once()

		// Docker не найден
		commander.On("LookPath", "docker").Return("", errors.New("not found")).Once()
		logger.On("Error", "Docker not found").Once()

		logger.On("Info", "Connecting to Chrome...").Once()
		logger.On("Error", "Failed to connect to Chrome container").Once()
		logger.On("Errorf", "Connection error: %v", mock.AnythingOfType("*errors.errorString")).Once()

		r := NewRenderer(logger, commander, httpClient)

		err := r.setupContainer()
		assert.Error(t, err)

		// Проверяем, что все ожидаемые вызовы были сделаны
		logger.AssertExpectations(t)
		commander.AssertExpectations(t)
		httpClient.AssertExpectations(t)
	})
}

func TestRestartContainer(t *testing.T) {
	t.Run("SuccessfulRestart", func(t *testing.T) {
		t.Log("Starting TestRestartContainer (SuccessfulRestart)")
		logger := new(MockLogger)
		commander := new(MockCommander)
		httpClient := new(MockHTTPClient)

		// Создаем рендерер
		r := NewRenderer(logger, commander, httpClient)
		r.dockerPath = "docker"

		// Мокируем команды для restartContainer()
		cmdStatus := exec.Command("echo", "exited")
		commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).
			Return(cmdStatus).Once()

		cmdRestart := exec.Command("echo")
		commander.On("Command", "docker", []string{"restart", containerName}).
			Return(cmdRestart).Once()

		cmdStatus2 := exec.Command("echo", "running")
		commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).
			Return(cmdStatus2).Once()

		// Мокируем HTTP-запрос после перезапуска
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       mockBody(`{"webSocketDebuggerUrl": "ws://localhost:9222/devtools/browser/new"}`),
		}
		httpClient.On("Get", debugURL).Return(resp, nil).Once()

		// Ожидания логирования
		logger.On("Info", "Waiting for active requests to complete before restart...").Once()
		logger.On("Info", "All active requests completed").Once()
		logger.On("Info", "Restarting container...").Once()
		logger.On("Warnf", "Container status: %s, restarting...", "exited").Once()
		logger.On("Info", "Container restarted successfully").Once()

		r.setRestarting(false)
		err := r.restartContainer()
		assert.NoError(t, err)

		// Проверяем, что все ожидаемые вызовы были сделаны
		logger.AssertExpectations(t)
		commander.AssertExpectations(t)
		httpClient.AssertExpectations(t)
	})

	t.Run("RestartSkippedDueToCooldown", func(t *testing.T) {
		t.Log("Starting TestRestartContainer (RestartSkippedDueToCooldown)")
		logger := new(MockLogger)
		commander := new(MockCommander)
		httpClient := new(MockHTTPClient)

		r := NewRenderer(logger, commander, httpClient)

		// Устанавливаем время последнего перезапуска
		r.lastRestart = time.Now().Add(-10 * time.Second)

		// Ожидание логирования
		logger.On("Warn", "Restart skipped: still in cooldown period").Once()

		err := r.restartContainer()
		assert.NoError(t, err)

		// Проверяем, что все ожидаемые вызовы были сделаны
		logger.AssertExpectations(t)
		commander.AssertExpectations(t)
		httpClient.AssertExpectations(t)
	})
}

func TestRenderPage(t *testing.T) {
	t.Run("InvalidContext", func(t *testing.T) {
		t.Log("Starting TestRenderPage (InvalidContext)")
		logger := new(MockLogger)
		commander := new(MockCommander)
		httpClient := new(MockHTTPClient)
		portChecker := new(MockPortChecker)

		// Настраиваем ожидания для вызовов в Setup()
		logger.On("Info", "Initializing renderer...").Once()
		logger.On("Info", "Setting up container...").Once()

		// Мокируем Docker
		commander.On("LookPath", "docker").Return("/usr/bin/docker", nil).Once()

		// Первый вызов getContainerStatus
		cmdStatus1 := exec.Command("echo", "running")
		commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus1).Once()
		logger.On("Infof", "Initial container status: %s", "running").Once()

		// Второй вызов getContainerStatus
		cmdStatus2 := exec.Command("echo", "running")
		commander.On("Command", "sh", []string{"-c", dockerHealthCheckCmd}).Return(cmdStatus2).Once()

		logger.On("Info", "Container setup completed").Once()
		logger.On("Info", "Connecting to Chrome...").Once()

		// Мокируем HTTP-запрос
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body:       mockBody(`{"webSocketDebuggerUrl": "ws://localhost:9222/devtools/browser/test"}`),
		}
		httpClient.On("Get", debugURL).Return(resp, nil).Once()

		// Мокируем проверку порта
		portChecker.On("IsPortAvailable", 9222).Return(true).Once()

		logger.On("Infof", "Using Chrome debug URL: %s", "ws://localhost:9222/devtools/browser/test").Once()
		logger.On("Info", "Connected to Chrome via remote allocator").Once()

		// Создаем рендерер
		r := &Renderer{
			logger:                logger,
			commander:             commander,
			httpClient:            httpClient,
			portChecker:           portChecker,
			semaphore:             make(chan struct{}, maxConcurrentRenders),
			restartQueue:          make(chan struct{}, 1),
			containerReadyTimeout: 60 * time.Second,
			containerStartDelay:   0,
			debugURLRetryDelay:    1 * time.Second,
			debugURLMaxAttempts:   15,
		}
		r.resetReadyCh()
		r.Setup()

		// Отменяем контекст
		r.allocatorCtx, r.cancelAllocator = context.WithCancel(context.Background())
		r.cancelAllocator()

		result := &RenderResult{}
		_, err := r.renderPage("https://example.com", result)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidContext))

		// Проверяем, что все ожидаемые вызовы были сделаны
		logger.AssertExpectations(t)
		commander.AssertExpectations(t)
		httpClient.AssertExpectations(t)
		portChecker.AssertExpectations(t)
	})
}

// renderer.go
package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stretchr/testify/mock"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Commander интерфейс для работы с системными командами
type Commander interface {
	LookPath(file string) (string, error)
	Command(name string, arg ...string) *exec.Cmd
}

// RealCommander реализация Commander для реального окружения
type RealCommander struct{}

func (c *RealCommander) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (c *RealCommander) Command(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

// HTTPClient интерфейс для HTTP-клиента
type HTTPClient interface {
	Get(url string) (*http.Response, error)
}

// RealHTTPClient реализация HTTPClient для реального окружения
type RealHTTPClient struct{}

func (c *RealHTTPClient) Get(url string) (*http.Response, error) {
	return http.Get(url)
}

// Logger интерфейс для логирования
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

// Constants
const (
	containerName         = "headless-shell"
	debugURL              = "http://localhost:9222/json/version"
	renderTimeout         = 60 * time.Second
	containerStartDelay   = 10 * time.Second
	containerReadyDelay   = 5 * time.Second
	maxRestartAttempts    = 3
	dockerHealthCheckCmd  = "docker inspect -f '{{.State.Status}}' " + containerName
	maxConcurrentRenders  = 10
	containerReadyTimeout = 60 * time.Second
	restartCooldown       = 15 * time.Second
)

// Errors
var (
	ErrNotResponding      = errors.New("chrome not responding")
	ErrNameNotResolved    = errors.New("domain name not resolved")
	ErrContainerRestart   = errors.New("container restart failed")
	ErrTimeoutExceeded    = errors.New("render timeout exceeded")
	ErrContainerNotReady  = errors.New("container not ready")
	ErrContainerStartFail = errors.New("container start failed")
	ErrContextCanceled    = errors.New("context canceled")
	ErrInvalidContext     = errors.New("invalid context")
)

// PortChecker интерфейс для проверки портов
type PortChecker interface {
	IsPortAvailable(port int) bool
}

// Добавляем мокированный sleeper
type MockSleeper struct {
	mock.Mock
}

func (m *MockSleeper) Sleep(d time.Duration) {
	m.Called(d)
}

// Renderer структура для управления процессом рендеринга
type Renderer struct {
	allocatorCtx          context.Context
	cancelAllocator       context.CancelFunc
	isRemote              bool
	isStarted             bool
	mutex                 sync.RWMutex
	restartMutex          sync.Mutex
	logger                Logger
	dockerPath            string
	lastRestart           time.Time
	captureConsoleLog     bool
	blockedURLs           []string
	restartingFlag        bool
	wsURL                 string
	semaphore             chan struct{}
	readyCh               chan struct{}
	readyMutex            sync.Mutex
	containerReady        bool
	activeRequests        sync.WaitGroup
	restartQueue          chan struct{}
	allocatorMutex        sync.RWMutex
	commander             Commander
	httpClient            HTTPClient
	containerReadyTimeout time.Duration
	containerStartDelay   time.Duration
	debugURLRetryDelay    time.Duration
	debugURLMaxAttempts   int
	portChecker           PortChecker
	sleeper               func(d time.Duration) // Добавляем это поле
}

// RenderResult результат рендеринга
type RenderResult struct {
	HTML       string
	Console    []ConsoleEntry
	Exception  string
	TotalTime  time.Duration
	RenderTime time.Duration
}

// ConsoleEntry запись консоли
type ConsoleEntry struct {
	Type     string
	Messages []string
}

// NewRenderer создает новый экземпляр Renderer
func NewRenderer(logger Logger, commander Commander, httpClient HTTPClient) *Renderer {
	r := &Renderer{
		logger: logger,
		blockedURLs: []string{
			"google-analytics.com",
			"mc.yandex.ru",
			"maps.googleapis.com",
			"googletagmanager.com",
			"api-maps.yandex.ru",
		},
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		commander:             commander,
		httpClient:            httpClient,
		containerReadyTimeout: 60 * time.Second, // Значение по умолчанию
		containerStartDelay:   10 * time.Second,
		debugURLRetryDelay:    1 * time.Second,
		debugURLMaxAttempts:   15,
	}
	r.resetReadyCh()
	r.Setup()
	return r
}

func (r *Renderer) setContainerReady(ready bool) {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()

	// Если состояние не изменилось, ничего не делаем
	if r.containerReady == ready {
		return
	}

	r.containerReady = ready
	if ready {
		// Устанавливаем готовность - закрываем канал только если он еще не закрыт
		if r.readyCh != nil {
			select {
			case <-r.readyCh:
				// Канал уже закрыт
			default:
				close(r.readyCh)
			}
		}
	} else {
		// Сбрасываем готовность - создаем новый канал
		r.resetReadyCh()
	}
}

func (r *Renderer) resetReadyCh() {
	// Старый канал не закрываем, просто заменяем новым
	r.readyCh = make(chan struct{})
}

// SetConsoleCapture включает или выключает захват консоли
func (r *Renderer) SetConsoleCapture(enabled bool) {
	r.captureConsoleLog = enabled
}

// DoRender выполняет рендеринг страницы
func (r *Renderer) DoRender(requestURL string) (*RenderResult, error) {
	const maxAttempts = 5
	result := &RenderResult{}
	startTime := time.Now()

	if err := r.waitForContainerReady(); err != nil {
		r.logger.Errorf("Container not ready: %v", err)
		return nil, fmt.Errorf("%w: %v", ErrContainerNotReady, err)
	}

	r.semaphore <- struct{}{}
	defer func() { <-r.semaphore }()

	r.activeRequests.Add(1)
	defer r.activeRequests.Done()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if r.isRestarting() {
			waitTime := time.Until(r.lastRestart.Add(restartCooldown))
			if waitTime > 0 {
				r.logger.Warnf("Container restart in progress, waiting %v... (attempt %d/%d)", waitTime, attempt, maxAttempts)
				select {
				case <-time.After(waitTime):
				case <-r.readyCh:
				}
			}
			continue
		}

		content, err := r.renderPage(requestURL, result)
		if err == nil {
			result.HTML = content
			result.TotalTime = time.Since(startTime)
			return result, nil
		}

		if errors.Is(err, context.Canceled) {
			r.logger.Warnf("Render canceled for %s: %v", requestURL, err)
			return nil, ErrContextCanceled
		}

		r.logger.Errorf("Render attempt failed (attempt %d): %v", attempt, err)

		if errors.Is(err, ErrNameNotResolved) {
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

// waitForContainerReady ожидает готовности контейнера
func (r *Renderer) waitForContainerReady() error {
	start := time.Now()
	waitTime := 10 * time.Millisecond

	for {
		if r.isContainerReady() {
			return nil
		}

		if time.Since(start) > r.containerReadyTimeout {
			return fmt.Errorf("timeout after %v", r.containerReadyTimeout)
		}

		r.logger.Warnf("Container not ready, waiting %v...", waitTime)
		// Используем sleeper если установлен, иначе стандартный sleep
		if r.sleeper != nil {
			r.sleeper(waitTime)
		} else {
			time.Sleep(waitTime)
		}

		// Увеличиваем время ожидания с ограничением
		newWaitTime := waitTime * 2
		if newWaitTime > 500*time.Millisecond {
			newWaitTime = 500 * time.Millisecond
		}
		waitTime = newWaitTime
	}
}

// isContainerReady проверяет готовность контейнера
func (r *Renderer) isContainerReady() bool {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()
	return r.containerReady
}

// renderPage выполняет рендеринг страницы
func (r *Renderer) renderPage(url string, result *RenderResult) (string, error) {
	r.allocatorMutex.RLock()
	defer r.allocatorMutex.RUnlock()

	if r.allocatorCtx == nil || r.allocatorCtx.Err() != nil {
		return "", ErrInvalidContext
	}

	tabCtx, cancelTab := chromedp.NewContext(r.allocatorCtx)
	defer cancelTab()

	ctx, cancel := context.WithTimeout(tabCtx, renderTimeout)
	defer cancel()

	if r.captureConsoleLog {
		r.captureConsoleEvents(ctx, result)
	}

	var htmlContent string
	tasks := chromedp.Tasks{
		network.SetBlockedURLS(r.blockedURLs),
		network.SetExtraHTTPHeaders(network.Headers{"X-Prerender": "1"}),
		chromedp.Navigate(url),
		chromedp.Sleep(500 * time.Millisecond),
		chromedp.OuterHTML("html", &htmlContent, chromedp.ByQuery),
	}

	start := time.Now()
	err := chromedp.Run(ctx, tasks)
	result.RenderTime = time.Since(start)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", ErrTimeoutExceeded
		}
		if errors.Is(err, context.Canceled) {
			return "", context.Canceled
		}
		if strings.Contains(err.Error(), "ERR_NAME_NOT_RESOLVED") {
			return "", ErrNameNotResolved
		}
		return "", err
	}
	return htmlContent, nil
}

// captureConsoleEvents захватывает события консоли
func (r *Renderer) captureConsoleEvents(ctx context.Context, result *RenderResult) {
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			entry := ConsoleEntry{Type: ev.Type.String()}
			for _, arg := range ev.Args {
				var msg string
				if arg.Description != "" {
					msg = arg.Description
				} else if arg.Value != nil {
					msg = fmt.Sprintf("%v", arg.Value)
				} else {
					msg = fmt.Sprintf("(%s)", arg.Type)
				}
				entry.Messages = append(entry.Messages, msg)
			}
			result.Console = append(result.Console, entry)
			r.logger.Debugf("Console.%s: %v", entry.Type, entry.Messages)

		case *runtime.EventExceptionThrown:
			result.Exception = ev.ExceptionDetails.Error()
			r.logger.Errorf("Exception: %s", result.Exception)
		}
	})
}

// shouldRestart определяет, нужно ли перезапускать контейнер
func (r *Renderer) shouldRestart(err error) bool {
	return strings.Contains(err.Error(), "could not dial \"ws:") ||
		strings.Contains(err.Error(), "exec: \"google-chrome\":") ||
		errors.Is(err, ErrInvalidContext)
}

// Setup настраивает рендерер
func (r *Renderer) Setup() {
	r.logger.Info("Initializing renderer...")

	if !r.isStarted {
		r.logger.Info("Setting up container...")
		if err := r.setupContainer(); err != nil {
			r.logger.Errorf("Container setup error: %v", err)
		}
	}

	r.logger.Info("Connecting to Chrome...")
	wsURL, err := r.getDebugURLWithRetry()
	if err == nil {
		r.logger.Infof("Using Chrome debug URL: %s", wsURL)
		r.setRemoteAllocator(wsURL)
		r.setContainerReady(true)
		r.logger.Info("Connected to Chrome via remote allocator")
	} else {
		r.logger.Error("Failed to connect to Chrome container")
		r.logger.Errorf("Connection error: %v", err) // Добавлено дополнительное логирование
		r.setContainerReady(false)
	}
}

// setRemoteAllocator устанавливает удаленный аллокатор
func (r *Renderer) setRemoteAllocator(wsURL string) {
	r.allocatorMutex.Lock()
	defer r.allocatorMutex.Unlock()

	if r.cancelAllocator != nil {
		r.cancelAllocator()
	}

	allocatorCtx, cancel := chromedp.NewRemoteAllocator(context.Background(), wsURL)
	r.allocatorCtx = allocatorCtx
	r.cancelAllocator = cancel
	r.wsURL = wsURL
	r.isRemote = true
}

// restartContainer перезапускает контейнер
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

	r.logger.Info("Waiting for active requests to complete before restart...")
	done := make(chan struct{})
	go func() {
		r.activeRequests.Wait()
		close(done)
	}()

	select {
	case <-done:
		r.logger.Info("All active requests completed")
	case <-time.After(15 * time.Second):
		r.logger.Warn("Timeout waiting for active requests, proceeding with restart")
	}

	r.logger.Info("Restarting container...")
	for i := 0; i < maxRestartAttempts; i++ {
		status := r.getContainerStatus()
		if status != "running" {
			r.logger.Warnf("Container status: %s, restarting...", status)
			cmd := r.commander.Command(r.dockerPath, "restart", containerName)
			if output, err := cmd.CombinedOutput(); err != nil {
				r.logger.Errorf("Restart failed: %s", output)
				time.Sleep(2 * time.Second)
				continue
			}

			// После перезапуска ждем и проверяем статус снова
			time.Sleep(containerStartDelay)
			status = r.getContainerStatus()
		}

		// Если контейнер запущен, пытаемся подключиться
		if status == "running" {
			wsURL, err := r.getDebugURLWithRetry()
			if err != nil {
				r.logger.Warnf("Failed to get debug URL: %v", err)
				continue
			}
			r.setRemoteAllocator(wsURL)
			r.setContainerReady(true)
			r.logger.Info("Container restarted successfully")
			return nil
		}
	}

	r.setContainerReady(true)
	return ErrContainerRestart
}

// getDebugURLWithRetry получает URL для отладки с повторными попытками
func (r *Renderer) getDebugURLWithRetry() (string, error) {
	attempt := 1
	delay := r.debugURLRetryDelay
	maxDelay := 10 * time.Second

	for attempt <= r.debugURLMaxAttempts {
		wsURL, err := r.getDebugURL()
		if err == nil {
			return wsURL, nil
		}

		r.logger.Debugf("Debug URL attempt failed (%d/%d): %v", attempt, r.debugURLMaxAttempts, err)

		if attempt < r.debugURLMaxAttempts {
			// Используем sleeper если установлен, иначе стандартный sleep
			if r.sleeper != nil {
				r.sleeper(delay)
			} else {
				time.Sleep(delay)
			}

			// Экспоненциальная задержка с ограничением
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
		attempt++
	}

	return "", fmt.Errorf("failed to get debug URL after %d attempts", r.debugURLMaxAttempts)
}

// setRestarting устанавливает флаг перезапуска
func (r *Renderer) setRestarting(state bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.restartingFlag = state
}

// isRestarting проверяет, выполняется ли перезапуск
func (r *Renderer) isRestarting() bool {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return r.restartingFlag
}

// setupContainer настраивает контейнер
func (r *Renderer) setupContainer() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.isStarted {
		return nil
	}

	path, err := r.commander.LookPath("docker")
	if err != nil {
		r.logger.Error("Docker not found")
		return err
	}
	r.dockerPath = path

	status := r.getContainerStatus()
	r.logger.Infof("Initial container status: %s", status)

	if status != "running" {
		r.logger.Warnf("Starting container, current status: %s", status)
		cmd := r.commander.Command(r.dockerPath, "start", containerName)
		output, err := cmd.CombinedOutput()
		if err != nil {
			errMsg := fmt.Errorf("start failed: %w\n%s", err, output)
			r.logger.Errorf("%v", errMsg)
			return errMsg
		}
	}

	time.Sleep(r.containerStartDelay)
	status = r.getContainerStatus()
	if status != "running" {
		return fmt.Errorf("container did not start, status: %s", status)
	}

	r.isStarted = true
	r.logger.Info("Container setup completed")
	return nil
}

// getContainerStatus возвращает статус контейнера
func (r *Renderer) getContainerStatus() string {
	cmd := r.commander.Command("sh", "-c", dockerHealthCheckCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.Trim(string(output), "' \n")
}

// RealPortChecker реализация PortChecker для реального окружения
type RealPortChecker struct{}

func (c *RealPortChecker) IsPortAvailable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// getDebugURL получает URL для отладки
func (r *Renderer) getDebugURL() (string, error) {
	if !r.portChecker.IsPortAvailable(9222) {
		return "", errors.New("debug port not available")
	}

	resp, err := r.httpClient.Get(debugURL)
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

	var data struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	if data.WebSocketDebuggerURL == "" {
		return "", errors.New("empty debug URL")
	}
	return data.WebSocketDebuggerURL, nil
}

// isPortAvailable проверяет доступность порта
func (r *Renderer) isPortAvailable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Cancel отменяет операции рендерера
func (r *Renderer) Cancel() {
	r.allocatorMutex.Lock()
	defer r.allocatorMutex.Unlock()

	if r.cancelAllocator != nil {
		r.cancelAllocator()
	}
}

package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Do(req *http.Request) (*http.Response, error)
}

// RealHTTPClient реализация HTTPClient для реального окружения
type RealHTTPClient struct{}

func (c *RealHTTPClient) Do(req *http.Request) (*http.Response, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	return client.Do(req)
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

// AllocatorCreator интерфейс для создания аллокатора
type AllocatorCreator interface {
	CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc)
}

// RealAllocatorCreator реализация AllocatorCreator для реального окружения
type RealAllocatorCreator struct{}

func (a *RealAllocatorCreator) CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc) {
	return chromedp.NewRemoteAllocator(ctx, url)
}

// Constants
const (
	containerName         = "headless-shell"
	debugURL              = "http://localhost:9222/json/version"
	renderTimeout         = 120 * time.Second
	containerStartDelay   = 30 * time.Second
	containerReadyDelay   = 5 * time.Second
	maxRestartAttempts    = 5
	dockerHealthCheckCmd  = "docker inspect -f '{{.State.Status}}' " + containerName
	maxConcurrentRenders  = 10
	containerReadyTimeout = 180 * time.Second
	restartCooldown       = 60 * time.Second
	portCheckTimeout      = 10 * time.Second
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
	ErrPortNotAvailable   = errors.New("debug port not available")
	ErrChromeNotReady     = errors.New("chrome not ready")
)

// PortChecker интерфейс для проверки портов
type PortChecker interface {
	IsPortAvailable(port int) bool
}

// PageRenderer интерфейс для рендеринга страниц
type PageRenderer interface {
	RenderPage(url string, result *RenderResult) (string, error)
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
	restartQueue          chan struct{}
	allocatorMutex        sync.RWMutex
	commander             Commander
	httpClient            HTTPClient
	containerReadyTimeout time.Duration
	containerStartDelay   time.Duration
	debugURLRetryDelay    time.Duration
	debugURLMaxAttempts   int
	portChecker           PortChecker
	sleeper               func(d time.Duration)
	pageRenderer          PageRenderer // Стратегия рендеринга страниц
	allocatorCreator      AllocatorCreator
	activeRequests        int32 // Используем атомарный счетчик
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
			"doubleclick.net",
			"facebook.net",
		},
		semaphore:             make(chan struct{}, maxConcurrentRenders),
		restartQueue:          make(chan struct{}, 1),
		commander:             commander,
		httpClient:            httpClient,
		containerReadyTimeout: containerReadyTimeout,
		containerStartDelay:   containerStartDelay,
		debugURLRetryDelay:    1 * time.Second,
		debugURLMaxAttempts:   20,
		allocatorCreator:      &RealAllocatorCreator{},
	}
	r.resetReadyCh()
	r.pageRenderer = r // Use self as default renderer
	return r
}

// SetPageRenderer устанавливает кастомный рендерер страниц
func (r *Renderer) SetPageRenderer(pr PageRenderer) {
	r.pageRenderer = pr
}

func (r *Renderer) SetConcurrencyLimit(limit int) {
	r.semaphore = make(chan struct{}, limit)
}

// SetPortChecker устанавливает PortChecker
func (r *Renderer) SetPortChecker(pc PortChecker) {
	r.portChecker = pc
}

func (r *Renderer) setContainerReady(ready bool) {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()
	if r.containerReady == ready {
		return
	}
	r.containerReady = ready
	if ready {
		if r.readyCh != nil {
			select {
			case <-r.readyCh:
			default:
				close(r.readyCh)
			}
		}
	} else {
		r.resetReadyCh()
	}
}

func (r *Renderer) resetReadyCh() {
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

	// Проверяем валидность URL
	if !isValidURL(requestURL) {
		return nil, fmt.Errorf("invalid URL: %s", requestURL)
	}

	if err := r.waitForContainerReady(); err != nil {
		r.logger.Errorf("Container not ready: %v", err)
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
				r.logger.Warnf("Container restart in progress, waiting %v... (attempt %d/%d)", waitTime, attempt, maxAttempts)
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

// Вспомогательная функция для проверки URL
func isValidURL(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

// RenderPage реализация PageRenderer для рендеринга страниц
func (r *Renderer) RenderPage(url string, result *RenderResult) (string, error) {
	// Специальный URL для тестирования перезапуска контейнера
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

	ctx, cancel := context.WithTimeout(tabCtx, renderTimeout)
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
		// Добавляем проверку загрузки страницы
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(1 * time.Second), // Дополнительное время для JS
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
		if r.sleeper != nil {
			r.sleeper(waitTime)
		} else {
			time.Sleep(waitTime)
		}

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

		default:
			// Игнорируем неизвестные события
			return
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
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.isStarted {
		r.logger.Info("Renderer already initialized")
		return
	}

	r.logger.Info("Initializing renderer...")
	r.logger.Info("Setting up container...")
	if err := r.setupContainer(); err != nil {
		r.logger.Errorf("Container setup error: %v", err)
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
		r.logger.Errorf("Connection error: %v", err)
		r.setContainerReady(false)
	}
}

func (r *Renderer) SetContainerReadyTimeout(timeout time.Duration) {
	r.containerReadyTimeout = timeout
}

func (r *Renderer) SetDebugURLMaxAttempts(attempts int) {
	r.debugURLMaxAttempts = attempts
}

func (r *Renderer) IsContainerReady() bool {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()
	return r.containerReady
}

// setRemoteAllocator устанавливает удаленный аллокатор
func (r *Renderer) setRemoteAllocator(wsURL string) {
	r.allocatorMutex.Lock()
	defer r.allocatorMutex.Unlock()

	if r.cancelAllocator != nil {
		r.cancelAllocator()
	}

	// Используем контекст без таймаута для создания аллокатора
	allocatorCtx, cancelAlloc := r.allocatorCreator.CreateRemoteAllocator(context.Background(), wsURL)
	r.allocatorCtx = allocatorCtx
	r.cancelAllocator = cancelAlloc
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

	// Ожидаем завершения активных запросов с таймаутом
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	completed := false
	for !completed {
		select {
		case <-ctx.Done():
			r.logger.Warn("Timeout waiting for active requests, proceeding with restart")
			completed = true
		case <-ticker.C:
			if atomic.LoadInt32(&r.activeRequests) == 1 {
				r.logger.Info("All active requests completed")
				completed = true
			}
		}
	}

	r.logger.Info("Restarting container...")

	// ЗАМЕНА БЛОКА ОЧИСТКИ ПОРТА:
	if r.portChecker != nil && !r.portChecker.IsPortAvailable(9222) {
		r.logger.Warn("Debug port 9222 is busy, attempting to kill processes...")
		if rt.GOOS != "windows" {
			cmd := r.commander.Command("fuser", "-k", "9222/tcp")
			if err := cmd.Run(); err != nil {
				r.logger.Errorf("Failed to kill processes: %v", err)
			}
		} else {
			r.logger.Warn("Automatic port cleanup not supported on Windows")
		}
	}

	for i := 0; i < maxRestartAttempts; i++ {
		// Принудительно останавливаем контейнер перед запуском
		cmd := r.commander.Command(r.dockerPath, "stop", containerName)
		if output, err := cmd.CombinedOutput(); err != nil {
			r.logger.Warnf("Force stop failed: %s", string(output))
		}

		// Запускаем контейнер
		cmd = r.commander.Command(r.dockerPath, "start", containerName)
		if output, err := cmd.CombinedOutput(); err != nil {
			r.logger.Errorf("Start failed: %s", string(output))
			time.Sleep(2 * time.Second)
			continue
		}

		// Увеличиваем время ожидания готовности Chrome
		r.logger.Info("Waiting for Chrome to initialize...")
		time.Sleep(10 * time.Second)

		// Проверяем статус контейнера
		status := r.getContainerStatus()
		if status != "running" {
			r.logger.Warnf("Container status after start: %s, retrying...", status)
			continue
		}

		// Получаем новый debug URL
		wsURL, err := r.getDebugURLWithRetry()
		if err != nil {
			r.logger.Warnf("Failed to get debug URL: %v", err)
			continue
		}

		// Устанавливаем новый аллокатор
		r.setRemoteAllocator(wsURL)
		r.setContainerReady(true)

		// Проверяем работоспособность
		if err := r.verifyChromeConnection(); err == nil {
			r.logger.Info("Container restarted and verified successfully")
			return nil
		} else {
			r.logger.Warnf("Chrome verification failed: %v", err)
		}
	}

	return ErrContainerRestart
}

// Новая функция для проверки соединения
func (r *Renderer) verifyChromeConnection() error {
	testCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var res string
	err := chromedp.Run(testCtx,
		chromedp.Navigate("about:blank"),
		chromedp.OuterHTML("html", &res),
	)

	if err != nil || res == "" {
		return fmt.Errorf("chrome connection test failed: %w", err)
	}
	return nil
}

// getDebugURLWithRetry получает URL для отладки с повторными попытками
func (r *Renderer) getDebugURLWithRetry() (string, error) {
	attempt := 1
	delay := r.debugURLRetryDelay
	maxDelay := 15 * time.Second

	for attempt <= r.debugURLMaxAttempts {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		wsURL, err := r.getDebugURL(ctx)
		cancel()

		if err == nil {
			return wsURL, nil
		}

		r.logger.Debugf("Debug URL attempt failed (%d/%d): %v", attempt, r.debugURLMaxAttempts, err)

		if attempt < r.debugURLMaxAttempts {
			if delay > 0 {
				if r.sleeper != nil {
					r.sleeper(delay)
				} else {
					time.Sleep(delay)
				}
			}

			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
		attempt++
	}

	return "", fmt.Errorf("failed to get debug URL after %d attempts", r.debugURLMaxAttempts)
}

// getDebugURL получает URL для отладки
func (r *Renderer) getDebugURL(ctx context.Context) (string, error) {
	if r.portChecker != nil && !r.portChecker.IsPortAvailable(9222) {
		return "", ErrPortNotAvailable
	}

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

	// Если контейнер уже запущен, сразу возвращаем успех
	if status == "running" {
		r.isStarted = true
		r.logger.Info("Container setup completed")
		return nil
	}

	if r.sleeper != nil {
		r.sleeper(r.containerStartDelay)
	} else {
		time.Sleep(r.containerStartDelay)
	}
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
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), portCheckTimeout)
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

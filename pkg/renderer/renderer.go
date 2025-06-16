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

// Константы сервиса
const (
	defaultContainerName    = "headless-shell"
	defaultDebugPort        = 9222
	renderTimeout           = 120 * time.Second
	maxRestartAttempts      = 5
	maxConcurrentRenders    = 10
	containerReadyTimeout   = 180 * time.Second
	restartCooldown         = 60 * time.Second
	portCheckTimeout        = 10 * time.Second
	activeRequestsWaitLimit = 5 * time.Second
)

// Ошибки сервиса
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

// Logger интерфейс для унифицированного логирования
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

// Commander интерфейс для работы с системными командами
type Commander interface {
	LookPath(file string) (string, error)
	Command(name string, arg ...string) *exec.Cmd
}

// HTTPClient интерфейс для HTTP-запросов
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// AllocatorCreator интерфейс для создания контекста Chrome
type AllocatorCreator interface {
	CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc)
}

// PortChecker интерфейс для проверки доступности портов
type PortChecker interface {
	IsPortAvailable(port int) bool
}

// PageRenderer интерфейс для рендеринга страниц
type PageRenderer interface {
	RenderPage(url string, result *RenderResult) (string, error)
}

// ContainerManager интерфейс для управления контейнерами
type ContainerManager interface {
	EnsureRunning() error
	GetStatus() string
	Restart() error
}

// RenderResult содержит результаты рендеринга
type RenderResult struct {
	HTML       string         // HTML-содержимое страницы
	Console    []ConsoleEntry // Записи консоли браузера
	Exception  string         // Исключения JavaScript
	TotalTime  time.Duration  // Общее время выполнения
	RenderTime time.Duration  // Время непосредственно рендеринга
}

// ConsoleEntry представляет запись в консоли браузера
type ConsoleEntry struct {
	Type     string   // Тип записи (log, error, warning)
	Messages []string // Сообщения
}

// Renderer основной сервис рендеринга
type Renderer struct {
	// Состояние рендерера
	mutex          sync.Mutex // Защита общего состояния
	isStarted      bool       // Флаг инициализации
	restartingFlag bool       // Флаг выполнения перезапуска
	containerReady bool       // Флаг готовности контейнера

	// Контекст Chrome
	allocatorCtx    context.Context    // Контекст для Chrome DevTools
	cancelAllocator context.CancelFunc // Функция отмены контекста
	wsURL           string             // WebSocket URL для Chrome
	allocatorMutex  sync.RWMutex       // Мьютекс для аллокатора

	// Управление параллелизмом
	semaphore      chan struct{} // Семафор для ограничения параллелизма
	restartQueue   chan struct{} // Очередь перезапусков
	activeRequests int32         // Счетчик активных запросов
	restartMutex   sync.Mutex    // Мьютекс для синхронизации перезапуска

	// Каналы и блокировки
	readyCh    chan struct{} // Канал готовности контейнера
	readyMutex sync.Mutex    // Мьютекс для readyCh

	// Внешние зависимости
	logger           Logger           // Логгер
	commander        Commander        // Исполнитель команд
	httpClient       HTTPClient       // HTTP-клиент
	portChecker      PortChecker      // Проверка портов
	pageRenderer     PageRenderer     // Рендерер страниц
	allocatorCreator AllocatorCreator // Создатель контекста Chrome
	containerManager ContainerManager // Менеджер контейнеров

	// Конфигурация
	dockerPath            string        // Путь к Docker
	lastRestart           time.Time     // Время последнего перезапуска
	captureConsoleLog     bool          // Флаг захвата логов консоли
	blockedURLs           []string      // Блокируемые URL (трекинг, реклама)
	containerReadyTimeout time.Duration // Таймаут готовности контейнера
	debugURLRetryDelay    time.Duration // Задержка между попытками
	debugURLMaxAttempts   int           // Макс. попыток получения debug URL
	containerName         string        // Название контейнера
	debugPort             int           // Порт для отладки Chrome
}

// DefaultLogger стандартная реализация логгера
type DefaultLogger struct{}

func (l *DefaultLogger) Info(args ...interface{}) {
	log.Println(append([]interface{}{"[INFO]"}, args...)...)
}
func (l *DefaultLogger) Infof(format string, args ...interface{}) {
	log.Printf("[INFO] "+format, args...)
}
func (l *DefaultLogger) Warn(args ...interface{}) {
	log.Println(append([]interface{}{"[WARN]"}, args...)...)
}
func (l *DefaultLogger) Warnf(format string, args ...interface{}) {
	log.Printf("[WARN] "+format, args...)
}
func (l *DefaultLogger) Error(args ...interface{}) {
	log.Println(append([]interface{}{"[ERROR]"}, args...)...)
}
func (l *DefaultLogger) Errorf(format string, args ...interface{}) {
	log.Printf("[ERROR] "+format, args...)
}
func (l *DefaultLogger) Debug(args ...interface{}) {
	log.Println(append([]interface{}{"[DEBUG]"}, args...)...)
}
func (l *DefaultLogger) Debugf(format string, args ...interface{}) {
	log.Printf("[DEBUG] "+format, args...)
}

// RealCommander реализация для работы с ОС
type RealCommander struct{}

func (c *RealCommander) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}
func (c *RealCommander) Command(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

// RealHTTPClient стандартная реализация HTTP-клиента
type RealHTTPClient struct{}

func (c *RealHTTPClient) Do(req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}

// RealAllocatorCreator реализация для работы с Chrome DevTools
type RealAllocatorCreator struct{}

func (a *RealAllocatorCreator) CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc) {
	return chromedp.NewRemoteAllocator(ctx, url)
}

// RealPortChecker проверка портов через net.Dial
type RealPortChecker struct{}

func (c *RealPortChecker) IsPortAvailable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// DockerContainerManager реализация управления контейнерами Docker
type DockerContainerManager struct {
	commander     Commander
	containerName string
	debugPort     int
	logger        Logger
}

func (d *DockerContainerManager) dockerHealthCheckCmd() string {
	return fmt.Sprintf("docker inspect -f '{{.State.Status}}' %s", d.containerName)
}

func (d *DockerContainerManager) GetStatus() string {
	cmd := d.commander.Command("sh", "-c", d.dockerHealthCheckCmd())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.Trim(string(output), "' \n")
}

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

func (d *DockerContainerManager) Restart() error {
	d.logger.Infof("Restarting container %s...", d.containerName)
	cmd := d.commander.Command("docker", "restart", d.containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restart container: %v\n%s", err, output)
	}
	d.logger.Infof("Container %s restarted successfully", d.containerName)
	return nil
}

// NewRenderer создает новый экземпляр рендерера
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

// NewDefaultRenderer создает рендерер с зависимостями по умолчанию
func NewDefaultRenderer() *Renderer {
	logger := &DefaultLogger{}
	commander := &RealCommander{}
	httpClient := &RealHTTPClient{}
	return NewRenderer(logger, commander, httpClient)
}

/******************************************
 * Публичные методы для конфигурации
 ******************************************/

// SetBlockedURLs устанавливает пользовательский список блокируемых URL
func (r *Renderer) SetBlockedURLs(urls []string) {
	r.blockedURLs = urls
}

// SetPageRenderer устанавливает кастомный рендерер страниц
func (r *Renderer) SetPageRenderer(pr PageRenderer) {
	r.pageRenderer = pr
}

// SetPortChecker устанавливает кастомную проверку портов
func (r *Renderer) SetPortChecker(pc PortChecker) {
	r.portChecker = pc
}

// SetConcurrencyLimit устанавливает лимит параллельных запросов
func (r *Renderer) SetConcurrencyLimit(limit int) {
	r.semaphore = make(chan struct{}, limit)
}

// SetConsoleCapture включает/выключает захват логов консоли
func (r *Renderer) SetConsoleCapture(enabled bool) {
	r.captureConsoleLog = enabled
}

// SetContainerReadyTimeout устанавливает таймаут готовности контейнера
func (r *Renderer) SetContainerReadyTimeout(timeout time.Duration) {
	r.containerReadyTimeout = timeout
}

// SetDebugURLMaxAttempts устанавливает количество попыток получения debug URL
func (r *Renderer) SetDebugURLMaxAttempts(attempts int) {
	r.debugURLMaxAttempts = attempts
}

// SetContainerName устанавливает название контейнера
func (r *Renderer) SetContainerName(name string) {
	r.containerName = name
	if manager, ok := r.containerManager.(*DockerContainerManager); ok {
		manager.containerName = name
	}
}

// SetDebugPort устанавливает порт для отладки Chrome
func (r *Renderer) SetDebugPort(port int) {
	r.debugPort = port
	if manager, ok := r.containerManager.(*DockerContainerManager); ok {
		manager.debugPort = port
	}
}

// GetContainerName возвращает название контейнера
func (r *Renderer) GetContainerName() string {
	return r.containerName
}

// ForceRecovery принудительно восстанавливает работу рендерера
func (r *Renderer) ForceRecovery() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.logger.Info("Initiating forced recovery")
	r.Cancel()
	r.setContainerReady(false)
	r.resetReadyCh()

	// Повторная инициализация
	r.Setup()
}

/******************************************
 * Основные публичные методы
 ******************************************/

// Setup инициализирует рендерер
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

// DoRender выполняет рендеринг страницы
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

// IsContainerReady проверяет готовность контейнера
func (r *Renderer) IsContainerReady() bool {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()
	return r.containerReady
}

// Cancel прерывает все операции рендерера
func (r *Renderer) Cancel() {
	r.allocatorMutex.Lock()
	defer r.allocatorMutex.Unlock()

	if r.cancelAllocator != nil {
		r.cancelAllocator()
	}
}

/******************************************
 * Реализация PageRenderer (по умолчанию)
 ******************************************/

// RenderPage реализует рендеринг через Chrome DevTools Protocol
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
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(1 * time.Second),
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
		return "", err
	}
	return htmlContent, nil
}

/******************************************
 * Приватные методы управления состоянием
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
 * Методы управления контейнером
 ******************************************/

// restartContainer перезапускает контейнер Chrome
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

	// Отменяем текущий контекст
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

	// Используем менеджер контейнеров для перезапуска
	if err := r.containerManager.Restart(); err != nil {
		r.logger.Errorf("Container restart failed: %v", err)
		return err
	}

	// Очистка порта (только для Linux)
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
 * Вспомогательные методы
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
		errors.Is(err, ErrInvalidContext)
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

// Валидация URL
func isValidURL(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

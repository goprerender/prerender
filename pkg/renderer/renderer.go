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
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/goprerender/prerender/pkg/log"
)

const (
	containerName         = "headless-shell"
	debugURL              = "http://localhost:9222/json/version"
	renderTimeout         = 60 * time.Second
	containerStartDelay   = 10 * time.Second
	containerReadyDelay   = 2 * time.Second
	maxRestartAttempts    = 3
	dockerHealthCheckCmd  = "docker inspect -f '{{.State.Status}}' " + containerName
	maxConcurrentRenders  = 10
	containerReadyTimeout = 30 * time.Second
)

var (
	ErrNotResponding      = errors.New("chrome not responding")
	ErrNameNotResolved    = errors.New("domain name not resolved")
	ErrContainerRestart   = errors.New("container restart failed")
	ErrTimeoutExceeded    = errors.New("render timeout exceeded")
	ErrContainerNotReady  = errors.New("container not ready")
	ErrContainerStartFail = errors.New("container start failed")
)

type Renderer struct {
	allocatorCtx      context.Context
	cancelAllocator   context.CancelFunc
	isRemote          bool
	isStarted         bool
	mutex             sync.RWMutex
	restartMutex      sync.Mutex
	logger            log.Logger
	dockerPath        string
	lastRestart       time.Time
	captureConsoleLog bool
	blockedURLs       []string
	restartingFlag    bool
	wsURL             string
	semaphore         chan struct{}
	readyCh           chan struct{}
	readyMutex        sync.Mutex
	containerReady    bool // Переименовано поле (было isContainerReady)
}

type RenderResult struct {
	HTML       string
	Console    []ConsoleEntry
	Exception  string
	TotalTime  time.Duration
	RenderTime time.Duration
}

type ConsoleEntry struct {
	Type     string
	Messages []string
}

func NewRenderer(logger log.Logger) *Renderer {
	r := &Renderer{
		logger: logger,
		blockedURLs: []string{
			"google-analytics.com",
			"mc.yandex.ru",
			"maps.googleapis.com",
			"googletagmanager.com",
			"api-maps.yandex.ru",
		},
		semaphore: make(chan struct{}, maxConcurrentRenders),
		readyCh:   make(chan struct{}),
	}
	close(r.readyCh)
	r.Setup()
	return r
}

func (r *Renderer) SetConsoleCapture(enabled bool) {
	r.captureConsoleLog = enabled
}

func (r *Renderer) DoRender(requestURL string) (*RenderResult, error) {
	const maxAttempts = 5
	result := &RenderResult{}
	startTime := time.Now()

	// Ожидаем готовности контейнера с таймаутом
	if err := r.waitForContainerReady(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContainerNotReady, err)
	}

	// Ограничение одновременных запросов
	r.semaphore <- struct{}{}
	defer func() { <-r.semaphore }()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if r.isRestarting() {
			waitTime := time.Until(r.lastRestart.Add(containerStartDelay))
			if waitTime > 0 {
				r.logger.Warnf("Container restart in progress, waiting %v... (attempt %d/%d)", waitTime, attempt, maxAttempts)
				time.Sleep(waitTime)
			}
			continue
		}

		content, err := r.renderPage(requestURL, result)
		if err == nil {
			result.HTML = content
			result.TotalTime = time.Since(startTime)
			return result, nil
		}

		r.logger.Errorf("Render attempt failed (attempt %d): %v", attempt, err)

		if errors.Is(err, ErrNameNotResolved) || errors.Is(err, context.Canceled) {
			return nil, err
		}

		if r.shouldRestart(err) {
			if restartErr := r.restartContainer(); restartErr != nil {
				r.logger.Errorf("Container restart failed: %v", restartErr)
				time.Sleep(2 * time.Second)
			} else {
				time.Sleep(containerReadyDelay)
			}
		} else {
			time.Sleep(time.Second)
		}
	}

	return nil, fmt.Errorf("%w: all attempts failed for %s", ErrNotResponding, requestURL)
}

// Ожидает готовности контейнера с таймаутом
func (r *Renderer) waitForContainerReady() error {
	start := time.Now()
	waitTime := 1 * time.Second

	for {
		if r.isContainerReady() {
			return nil
		}

		if time.Since(start) > containerReadyTimeout {
			return fmt.Errorf("timeout after %v", containerReadyTimeout)
		}

		r.logger.Warnf("Container not ready, waiting %v...", waitTime)
		time.Sleep(waitTime)

		// Экспоненциальное увеличение времени ожидания
		waitTime *= 2
		if waitTime > 5*time.Second {
			waitTime = 5 * time.Second
		}
	}
}

// Проверяет готовность контейнера
func (r *Renderer) isContainerReady() bool {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()
	return r.containerReady
}

// Обновляет статус готовности контейнера
func (r *Renderer) setContainerReady(ready bool) {
	r.readyMutex.Lock()
	defer r.readyMutex.Unlock()

	if r.containerReady != ready {
		r.containerReady = ready
		if ready {
			// Сигнализируем о готовности
			select {
			case <-r.readyCh:
				// Уже закрыт, создаем новый
				r.readyCh = make(chan struct{})
				close(r.readyCh)
			default:
				close(r.readyCh)
			}
		} else {
			// Сбрасываем канал для ожидания
			r.readyCh = make(chan struct{})
		}
	}
}

func (r *Renderer) renderPage(url string, result *RenderResult) (string, error) {
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
		if strings.Contains(err.Error(), "ERR_NAME_NOT_RESOLVED") {
			return "", ErrNameNotResolved
		}
		return "", err
	}
	return htmlContent, nil
}

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

func (r *Renderer) shouldRestart(err error) bool {
	return strings.Contains(err.Error(), "could not dial \"ws:") ||
		strings.Contains(err.Error(), "exec: \"google-chrome\":")
}

func (r *Renderer) Setup() {
	if !r.isStarted {
		if err := r.setupContainer(); err != nil {
			r.logger.Warnf("Container setup error: %v", err)
		}
	}

	wsURL, err := r.getDebugURLWithRetry()
	if err == nil {
		r.wsURL = wsURL
		r.allocatorCtx, r.cancelAllocator = chromedp.NewRemoteAllocator(context.Background(), wsURL)
		r.isRemote = true
		r.setContainerReady(true)
		r.logger.Info("Connected to Chrome via remote allocator")
	} else {
		r.logger.Warn("Using local Chrome allocator")
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
		)
		r.allocatorCtx, r.cancelAllocator = chromedp.NewExecAllocator(context.Background(), opts...)
		r.isRemote = false
		r.setContainerReady(true)
	}
}

func (r *Renderer) restartContainer() error {
	r.restartMutex.Lock()
	defer r.restartMutex.Unlock()

	r.setRestarting(true)
	defer r.setRestarting(false)

	r.lastRestart = time.Now()
	r.setContainerReady(false) // Контейнер больше не готов

	r.logger.Info("Restarting container...")
	for i := 0; i < maxRestartAttempts; i++ {
		status := r.getContainerStatus()
		if status != "running" {
			r.logger.Warnf("Container status: %s, restarting...", status)
			cmd := exec.Command(r.dockerPath, "restart", containerName)
			if output, err := cmd.CombinedOutput(); err != nil {
				r.logger.Errorf("Restart failed: %s", output)
				time.Sleep(2 * time.Second)
				continue
			}
		}

		time.Sleep(containerStartDelay)

		// Получаем новый WebSocket URL с повторными попытками
		wsURL, err := r.getDebugURLWithRetry()
		if err != nil {
			r.logger.Warnf("Failed to get debug URL: %v", err)
			continue
		}

		// Пересоздаем аллокатор
		if r.cancelAllocator != nil {
			r.cancelAllocator()
		}
		r.allocatorCtx, r.cancelAllocator = chromedp.NewRemoteAllocator(context.Background(), wsURL)
		r.wsURL = wsURL
		r.isRemote = true
		r.setContainerReady(true) // Контейнер снова готов
		r.logger.Info("Container restarted successfully")
		return nil
	}

	// Даже если перезапуск не удался, помечаем контейнер как готовый
	r.setContainerReady(true)
	return ErrContainerRestart
}

func (r *Renderer) setRestarting(state bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.restartingFlag = state
}

func (r *Renderer) isRestarting() bool {
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return r.restartingFlag
}

func (r *Renderer) setupContainer() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.isStarted {
		return nil
	}

	path, err := exec.LookPath("docker")
	if err != nil {
		r.logger.Error("Docker not found")
		return err
	}
	r.dockerPath = path

	if status := r.getContainerStatus(); status != "running" {
		r.logger.Warnf("Starting container, current status: %s", status)
		cmd := exec.Command(r.dockerPath, "start", containerName)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("start failed: %w\n%s", err, output)
		}
	}

	r.isStarted = true
	r.setContainerReady(true)
	return nil
}

func (r *Renderer) getContainerStatus() string {
	cmd := exec.Command("sh", "-c", dockerHealthCheckCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.Trim(string(output), "' \n")
}

func (r *Renderer) getDebugURLWithRetry() (string, error) {
	const maxAttempts = 10
	const delay = 1 * time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		wsURL, err := r.getDebugURL()
		if err == nil {
			return wsURL, nil
		}

		r.logger.Debugf("Debug URL attempt failed (%d/%d): %v", attempt, maxAttempts, err)
		time.Sleep(delay)
	}

	return "", fmt.Errorf("failed to get debug URL after %d attempts", maxAttempts)
}

func (r *Renderer) getDebugURL() (string, error) {
	// Проверяем доступность debug-порта
	if !r.isPortAvailable(9222) {
		return "", errors.New("debug port not available")
	}

	resp, err := http.Get(debugURL)
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

func (r *Renderer) isPortAvailable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (r *Renderer) Cancel() {
	if r.cancelAllocator != nil {
		r.cancelAllocator()
	}
}

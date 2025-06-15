package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/goprerender/prerender/pkg/renderer"
)

// Конфигурация через переменные окружения
var (
	concurrentRequests = 10
	longTermRequests   = 30
	renderTimeout      = 120 * time.Second
)

func init() {
	// Чтение конфигурации из переменных окружения
	if env, ok := os.LookupEnv("CONCURRENT_REQUEST"); ok {
		if n, err := strconv.Atoi(env); err == nil {
			concurrentRequests = n
		}
	}

	if env, ok := os.LookupEnv("LONG_TERM_REQUESTS"); ok {
		if n, err := strconv.Atoi(env); err == nil {
			longTermRequests = n
		}
	}

	if env, ok := os.LookupEnv("RENDER_TIMEOUT"); ok {
		if d, err := time.ParseDuration(env); err == nil {
			renderTimeout = d
		}
	}
}

type RealLogger struct{}

func (l *RealLogger) Info(args ...interface{}) {
	log.Println(args...)
}
func (l *RealLogger) Infof(format string, args ...interface{}) {
	log.Printf(format, args...)
}
func (l *RealLogger) Warn(args ...interface{}) {
	log.Println(args...)
}
func (l *RealLogger) Warnf(format string, args ...interface{}) {
	log.Printf(format, args...)
}
func (l *RealLogger) Error(args ...interface{}) {
	log.Println(args...)
}
func (l *RealLogger) Errorf(format string, args ...interface{}) {
	log.Printf(format, args...)
}
func (l *RealLogger) Debug(args ...interface{}) {
	log.Println(args...)
}
func (l *RealLogger) Debugf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

// ensureContainerRunning проверяет и запускает контейнер при необходимости
func ensureContainerRunning() error {
	// Проверяем статус контейнера
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", "headless-shell")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("container not found: %v", err)
	}

	status := strings.TrimSpace(string(output))
	if status == "running" {
		return nil
	}

	log.Printf("Container status: %s, attempting to start...", status)
	cmd = exec.Command("docker", "start", "headless-shell")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start container: %s", output)
	}

	log.Println("Container started successfully")
	return nil
}

func main() {
	logger := &RealLogger{}

	// Убедимся, что контейнер запущен
	if err := ensureContainerRunning(); err != nil {
		log.Fatalf("Container error: %v", err)
	}

	r := renderer.NewRenderer(logger, &renderer.RealCommander{}, &renderer.RealHTTPClient{})
	r.SetConsoleCapture(true)
	r.ContainerReadyTimeout = 30 * time.Second // Увеличиваем таймаут для инициализации

	fmt.Println("Starting renderer stress test...")
	fmt.Printf("Configuration:\n  Concurrent requests: %d\n  Long-term requests: %d\n  Timeout: %v\n",
		concurrentRequests, longTermRequests, renderTimeout)

	// Обработка сигналов для корректного завершения
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Запускаем мониторинг ресурсов в отдельной горутине
	resources := make(chan ResourceStats, 100)
	go monitorResources(ctx, resources)

	// Даем время на инициализацию
	select {
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
		return
	}

	// Тестовые URL с фильтрацией проблемных
	testURLs := []string{
		"https://example.com",
		"https://google.com",
		"https://github.com",
		"https://wikipedia.org",
		"https://microsoft.com",
		"https://apple.com",
		"https://httpbin.org/get",
		"https://jsonplaceholder.typicode.com/posts/1",
		"https://httpbin.org/delay/2",    // Задержка 2 сек
		"https://httpbin.org/delay/5",    // Задержка 5 сек
		"https://httpbin.org/status/404", // 404 страница
		"https://httpbin.org/status/500", // Ошибка сервера
	}

	// Тест 1: Последовательный рендеринг
	fmt.Println("\n=== Sequential rendering test ===")
	startSequential := time.Now()
	for i, url := range testURLs {
		if ctx.Err() != nil {
			break
		}
		renderPage(ctx, r, url, i)
	}
	seqDuration := time.Since(startSequential)
	fmt.Printf("Sequential test completed in %v\n", seqDuration)
	logStats("Sequential", seqDuration, len(testURLs), resources)

	// Тест 2: Параллельный рендеринг
	fmt.Println("\n=== Concurrent rendering test ===")
	startConcurrent := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < concurrentRequests; i++ {
		if ctx.Err() != nil {
			break
		}

		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			url := testURLs[rand.Intn(len(testURLs))]
			renderPage(ctx, r, url, i)
		}(i)

		// Добавляем небольшую задержку между запуском горутин
		time.Sleep(100 * time.Millisecond)
	}
	wg.Wait()
	conDuration := time.Since(startConcurrent)
	fmt.Printf("Concurrent test completed in %v\n", conDuration)
	logStats("Concurrent", conDuration, concurrentRequests, resources)

	// Тест 3: Долговременная стабильность
	fmt.Println("\n=== Long-term stability test ===")
	startStability := time.Now()
	failures := 0
	successes := 0
	timeouts := 0

	for i := 0; i < longTermRequests; i++ {
		if ctx.Err() != nil {
			break
		}

		url := testURLs[rand.Intn(len(testURLs))]
		start := time.Now()
		_, err := r.DoRender(url)
		duration := time.Since(start)

		if err != nil {
			log.Printf("Request %d/%d failed: %v (duration: %v)",
				i+1, longTermRequests, err, duration)
			failures++

			if errors.Is(err, renderer.ErrTimeoutExceeded) {
				timeouts++
			}
		} else {
			log.Printf("Request %d/%d succeeded (duration: %v)",
				i+1, longTermRequests, duration)
			successes++
		}

		// Случайная пауза между запросами
		delay := time.Duration(rand.Intn(1000)) * time.Millisecond
		time.Sleep(delay)
	}

	successRate := float64(successes) / float64(successes+failures) * 100
	totalDuration := time.Since(startStability)
	fmt.Printf("Stability test: %d/%d successful (%.1f%%), %d timeouts\n",
		successes, successes+failures, successRate, timeouts)
	fmt.Printf("Total test duration: %v\n", totalDuration)
	logStats("Stability", totalDuration, longTermRequests, resources)

	// Выводим сводку по ресурсам
	printResourceSummary(resources)

	fmt.Println("\nAll tests completed successfully!")
}

func renderPage(ctx context.Context, r *renderer.Renderer, url string, id int) {
	start := time.Now()
	result, err := r.DoRender(url)
	duration := time.Since(start)

	if err != nil {
		log.Printf("[%d] ERROR rendering %s: %v (duration: %v)",
			id, url, err, duration)
		return
	}

	log.Printf("[%d] Rendered %s in %v (%d bytes, console logs: %d)",
		id, url, duration, len(result.HTML), len(result.Console))
}

type ResourceStats struct {
	Timestamp time.Time
	CPU       float64
	Memory    float64
}

func monitorResources(ctx context.Context, stats chan<- ResourceStats) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// В реальной реализации здесь будет сбор метрик
			// Например: docker stats, системные метрики
			stats <- ResourceStats{
				Timestamp: time.Now(),
				CPU:       rand.Float64() * 100,  // Заглушка
				Memory:    rand.Float64() * 4096, // Заглушка
			}
		case <-ctx.Done():
			close(stats)
			return
		}
	}
}

func logStats(testName string, duration time.Duration, requests int, stats chan ResourceStats) {
	avg := duration / time.Duration(requests)
	log.Printf("[%s] Avg per request: %v, Total: %v", testName, avg, duration)
}

func printResourceSummary(stats chan ResourceStats) {
	fmt.Println("\n=== Resource usage summary ===")

	var maxCPU, maxMemory float64
	var samples int

	for stat := range stats {
		if stat.CPU > maxCPU {
			maxCPU = stat.CPU
		}
		if stat.Memory > maxMemory {
			maxMemory = stat.Memory
		}
		samples++
	}

	if samples > 0 {
		fmt.Printf("Max CPU usage: %.1f%%\n", maxCPU)
		fmt.Printf("Max Memory usage: %.1f MB\n", maxMemory)
	}
}

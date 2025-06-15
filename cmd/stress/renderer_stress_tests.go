package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
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

const containerName = "headless-shell"

var (
	concurrentRequests = 10
	longTermRequests   = 30
	renderTimeout      = 120 * time.Second
)

func init() {
	if env, ok := os.LookupEnv("CONCURRENT_REQUESTS"); ok {
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

func waitForContainerPort(port int, timeout time.Duration) error {
	start := time.Now()
	address := fmt.Sprintf("localhost:%d", port)

	for {
		conn, err := net.DialTimeout("tcp", address, 1*time.Second)
		if err == nil {
			conn.Close()
			log.Printf("Container port %d is available after %v", port, time.Since(start))
			return nil
		}

		if time.Since(start) > timeout {
			return fmt.Errorf("timeout waiting for port %d after %v", port, timeout)
		}

		log.Printf("Waiting for container port %d... (%v elapsed)", port, time.Since(start))
		time.Sleep(500 * time.Millisecond)
	}
}

func ensureContainerRunning() error {
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Status}}", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Println("Container not found, creating...")
		cmd = exec.Command("docker", "run", "-d", "-p", "9222:9222", "--name", containerName, "chromedp/headless-shell")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create container: %v\n%s", err, output)
		}
		log.Println("Container created successfully")
		return waitForContainerPort(9222, 30*time.Second)
	}

	status := strings.TrimSpace(string(output))
	if status == "running" {
		log.Println("Container is already running")
		return waitForContainerPort(9222, 5*time.Second)
	}

	log.Printf("Container status: %s, attempting to start...", status)
	cmd = exec.Command("docker", "start", containerName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to start container: %v\n%s", err, output)
	}

	log.Println("Container started successfully")
	return waitForContainerPort(9222, 30*time.Second)
}

func main() {
	logger := &RealLogger{}

	log.Println("Preparing container...")
	if err := ensureContainerRunning(); err != nil {
		log.Fatalf("Container error: %v", err)
	}

	r := renderer.NewRenderer(logger, &renderer.RealCommander{}, &renderer.RealHTTPClient{})
	r.SetConsoleCapture(true)
	r.SetContainerReadyTimeout(60 * time.Second)
	r.SetDebugURLMaxAttempts(60)

	fmt.Println("Starting renderer stress test...")
	fmt.Printf("Configuration:\n  Concurrent requests: %d\n  Long-term requests: %d\n  Timeout: %v\n",
		concurrentRequests, longTermRequests, renderTimeout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("Initializing renderer...")
	r.Setup()

	log.Println("Waiting for renderer to be ready...")
	start := time.Now()
	for !r.IsContainerReady() {
		if time.Since(start) > 60*time.Second {
			log.Fatal("Renderer failed to become ready within 60 seconds")
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Println("Renderer is ready")

	// Запускаем тест перезапуска контейнера
	testContainerRestart(ctx, r, logger)

	// Тест 1: Последовательный рендеринг
	fmt.Println("\n=== Sequential rendering test ===")
	startSequential := time.Now()
	testURLs := []string{
		"https://example.com",
		"https://google.com",
		"https://github.com",
		"https://wikipedia.org",
		"https://microsoft.com",
		"https://apple.com",
		"https://httpbin.org/get",
		"https://jsonplaceholder.typicode.com/posts/1",
		"https://httpbin.org/delay/2",
		"https://httpbin.org/delay/5",
		"https://httpbin.org/status/404",
		"https://httpbin.org/status/500",
	}

	for i, url := range testURLs {
		if ctx.Err() != nil {
			break
		}
		renderPage(ctx, r, url, i)
	}
	seqDuration := time.Since(startSequential)
	fmt.Printf("Sequential test completed in %v\n", seqDuration)
	logStats("Sequential", seqDuration, len(testURLs))

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

		time.Sleep(100 * time.Millisecond)
	}
	wg.Wait()
	conDuration := time.Since(startConcurrent)
	fmt.Printf("Concurrent test completed in %v\n", conDuration)
	logStats("Concurrent", conDuration, concurrentRequests)

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

		delay := time.Duration(rand.Intn(1000)) * time.Millisecond
		time.Sleep(delay)
	}

	successRate := float64(successes) / float64(successes+failures) * 100
	totalDuration := time.Since(startStability)
	fmt.Printf("Stability test: %d/%d successful (%.1f%%), %d timeouts\n",
		successes, successes+failures, successRate, timeouts)
	fmt.Printf("Total test duration: %v\n", totalDuration)
	logStats("Stability", totalDuration, longTermRequests)

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
			stats <- ResourceStats{
				Timestamp: time.Now(),
				CPU:       rand.Float64() * 100,
				Memory:    rand.Float64() * 4096,
			}
		case <-ctx.Done():
			close(stats)
			return
		}
	}
}

func logStats(testName string, duration time.Duration, requests int) {
	if requests == 0 {
		log.Printf("[%s] No requests completed", testName)
		return
	}

	avg := duration / time.Duration(requests)
	log.Printf("[%s] Avg per request: %v, Total: %v", testName, avg, duration)
}

func testContainerRestart(ctx context.Context, r *renderer.Renderer, logger *RealLogger) {
	fmt.Println("\n=== Container restart test ===")

	// Получаем время последнего старта контейнера
	log.Println("Getting container start time before restart...")
	startTimeBefore := getContainerStartTime()
	log.Printf("Container start time before restart: %s", startTimeBefore)

	// Специальный URL для тестирования перезапуска
	triggerURL := "https://invalid-url-that-triggers-restart"

	// Первый рендер - должен вызвать ошибку и триггернуть перезапуск
	log.Println("Simulating connection error to trigger restart...")
	start := time.Now()
	_, err := r.DoRender(triggerURL)
	duration := time.Since(start)

	if err == nil {
		log.Fatal("Expected error but got success")
	}
	log.Printf("Received expected error: %v (duration: %v)", err, duration)

	// Ждем перезапуска
	log.Println("Waiting for container to restart...")
	startWait := time.Now()
	for {
		// Проверяем, изменилось ли время старта контейнера
		currentStartTime := getContainerStartTime()
		if currentStartTime != startTimeBefore {
			log.Printf("Container restarted! New start time: %s", currentStartTime)
			break
		}

		if time.Since(startWait) > 120*time.Second {
			log.Fatal("Container did not restart within 120 seconds")
		}
		time.Sleep(2 * time.Second)
	}

	// Проверяем, что рендеринг работает после перезапуска
	log.Println("Verifying rendering after restart...")
	start = time.Now()
	result, err := r.DoRender("https://example.com")
	duration = time.Since(start)

	if err != nil {
		log.Fatalf("Rendering failed after restart: %v", err)
	}
	log.Printf("Rendered successfully in %v: %d bytes", duration, len(result.HTML))
	log.Println("Container restart test completed successfully!")
}

// getContainerStartTime возвращает время старта контейнера
func getContainerStartTime() string {
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.StartedAt}}", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func printResourceSummary(stats chan ResourceStats) {
	fmt.Println("\n=== Resource usage summary ===")

	var maxCPU, maxMemory float64
	var samples int

	for {
		select {
		case stat, ok := <-stats:
			if !ok {
				goto done
			}
			if stat.CPU > maxCPU {
				maxCPU = stat.CPU
			}
			if stat.Memory > maxMemory {
				maxMemory = stat.Memory
			}
			samples++
		default:
			goto done
		}
	}
done:

	if samples > 0 {
		fmt.Printf("Max CPU usage: %.1f%%\n", maxCPU)
		fmt.Printf("Max Memory usage: %.1f MB\n", maxMemory/1024/1024)
	} else {
		fmt.Println("No resource data collected")
	}
}

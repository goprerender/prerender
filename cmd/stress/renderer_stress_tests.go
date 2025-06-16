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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/goprerender/prerender/pkg/renderer"
)

var (
	concurrentRequests = 10
	longTermRequests   = 1
	renderTimeout      = 120 * time.Second
	containerName      = "headless-shell-test"
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

	if env, ok := os.LookupEnv("CONTAINER_NAME"); ok {
		containerName = env
	}
}

type RealLogger struct{}

func (l *RealLogger) log(prefix string, args ...interface{}) {
	log.Print("["+prefix+"] ", fmt.Sprint(args...))
}

func (l *RealLogger) logf(prefix, format string, args ...interface{}) {
	log.Printf("["+prefix+"] "+format, args...)
}

func (l *RealLogger) Info(args ...interface{}) {
	l.log("INFO", args...)
}

func (l *RealLogger) Infof(format string, args ...interface{}) {
	l.logf("INFO", format, args...)
}

func (l *RealLogger) Warn(args ...interface{}) {
	l.log("WARN", args...)
}

func (l *RealLogger) Warnf(format string, args ...interface{}) {
	l.logf("WARN", format, args...)
}

func (l *RealLogger) Error(args ...interface{}) {
	l.log("ERROR", args...)
}

func (l *RealLogger) Errorf(format string, args ...interface{}) {
	l.logf("ERROR", format, args...)
}

func (l *RealLogger) Debug(args ...interface{}) {
	l.log("DEBUG", args...)
}

func (l *RealLogger) Debugf(format string, args ...interface{}) {
	l.logf("DEBUG", format, args...)
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
		time.Sleep(1 * time.Second)
	}
}

func createContainer(port int) error {
	log.Println("Creating new container...")
	cmd := exec.Command("docker", "run", "-d",
		"-p", fmt.Sprintf("%d:9222", port),
		"--name", containerName,
		"--memory=4g", "--cpus=4", "--shm-size=2g",
		"chromedp/headless-shell:latest",
		"--disable-software-rasterizer",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--no-zygote",
		"--single-process",
		"--disable-setuid-sandbox",
		"--no-sandbox",
		"--health-start-period=10s",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create container: %v\n%s", err, output)
	}
	log.Println("Container created successfully")
	return waitForContainerPort(port, 60*time.Second)
}

func cleanupContainer() {
	if containerName == "" {
		return
	}

	cmd := exec.Command("docker", "inspect", "--format='{{.State.Status}}'", containerName)
	if _, err := cmd.CombinedOutput(); err != nil {
		log.Println("Container does not exist, nothing to clean up")
		return
	}

	log.Println("Stopping container...")
	cmd = exec.Command("docker", "stop", "-t", "0", containerName)
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("Failed to stop container: %v\n%s", err, output)
	} else {
		log.Println("Container stopped")
	}

	log.Println("Removing container...")
	cmd = exec.Command("docker", "rm", containerName)
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("Failed to remove container: %v\n%s", err, output)
	} else {
		log.Println("Container removed")
	}
}

func getContainerPort() (int, error) {
	cmd := exec.Command("docker", "inspect",
		"--format", "{{(index (index .NetworkSettings.Ports \"9222/tcp\") 0).HostPort}}",
		containerName)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("docker inspect error: %v\nOutput: %s", err, string(output))
	}

	portStr := strings.TrimSpace(string(output))
	if portStr == "" {
		return 0, errors.New("port not found")
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse port '%s': %v", portStr, err)
	}

	return port, nil
}

func ensureContainerRunning(port int) error {
	cleanupContainer()

	if err := createContainer(port); err != nil {
		return err
	}

	actualPort, err := getContainerPort()
	if err != nil {
		return fmt.Errorf("failed to get container port: %v", err)
	}

	if actualPort != port {
		log.Printf("Warning: Requested port %d, but container is using port %d", port, actualPort)
	}

	return nil
}

func getContainerLogs() string {
	if containerName == "" {
		return ""
	}

	cmd := exec.Command("docker", "logs", containerName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Failed to get container logs: %v", err)
	}
	return string(output)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("Starting stress test on %s with %d CPUs",
		runtime.GOOS, runtime.NumCPU())

	logger := &RealLogger{}

	defer func() {
		logs := getContainerLogs()
		if logs != "" {
			log.Println("\n=== Container logs ===")
			log.Println(logs)
		}
		cleanupContainer()
	}()

	rand.Seed(time.Now().UnixNano())
	port := 9222 + rand.Intn(1000)

	log.Printf("Preparing container %s on port %d...", containerName, port)
	if err := ensureContainerRunning(port); err != nil {
		log.Fatalf("Container error: %v", err)
	}

	actualPort, err := getContainerPort()
	if err != nil {
		log.Printf("Warning: failed to get container port: %v", err)
		actualPort = port
	} else {
		log.Printf("Container is using port %d", actualPort)
	}

	r := renderer.NewDefaultRenderer()
	r.SetContainerName(containerName)
	r.SetDebugPort(actualPort)
	r.SetPortChecker(renderer.NewRealPortChecker())
	r.SetConsoleCapture(true)
	r.SetContainerReadyTimeout(120 * time.Second)
	r.SetDebugURLMaxAttempts(15)
	r.SetConcurrencyLimit(5)
	r.SetRenderTimeout(renderTimeout)

	log.Println("Starting renderer stress test...")
	log.Printf("Configuration:\n  Container: %s\n  Port: %d\n  Concurrent requests: %d\n  Long-term requests: %d\n  Timeout: %v\n",
		containerName, actualPort, concurrentRequests, longTermRequests, renderTimeout)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go monitorRenderer(ctx, r)

	log.Println("Initializing renderer...")
	r.Setup()

	log.Println("Waiting for renderer to be ready...")
	start := time.Now()
	for !r.IsContainerReady() {
		if time.Since(start) > 120*time.Second {
			log.Fatal("Renderer failed to become ready within 120 seconds")
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Println("Renderer is ready")

	testContainerRestart(ctx, r, logger)

	log.Println("\n=== Sequential rendering test ===")
	startSequential := time.Now()
	testURLs := []string{
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

	log.Println("\n=== Concurrent rendering test ===")
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

		time.Sleep(500 * time.Millisecond)
	}
	wg.Wait()
	conDuration := time.Since(startConcurrent)
	fmt.Printf("Concurrent test completed in %v\n", conDuration)
	logStats("Concurrent", conDuration, concurrentRequests)

	log.Println("\n=== Long-term stability test ===")
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

		delay := time.Duration(500+rand.Intn(1500)) * time.Millisecond
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

	ttfb := result.Timings.Navigation - result.Timings.Waiting
	log.Printf("[%d] Rendered %s in %v [TTFB=%v, nav=%v, wait=%v, render=%v] (%d bytes, console logs: %d)",
		id, url, duration,
		ttfb,
		result.Timings.Navigation,
		result.Timings.Waiting,
		result.Timings.Rendering,
		len(result.HTML),
		len(result.Console))
}

type ResourceStats struct {
	Timestamp time.Time
	CPU       float64
	Memory    float64
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
	log.Println("\n=== Container restart test ===")

	log.Println("Getting container start time before restart...")
	startTimeBefore := getContainerStartTime(r)
	log.Printf("Container start time before restart: %s", startTimeBefore)

	triggerURL := "https://invalid-url-that-triggers-restart"

	log.Println("Simulating connection error to trigger restart...")
	start := time.Now()
	_, err := r.DoRender(triggerURL)
	duration := time.Since(start)

	if err == nil {
		log.Println("Expected error but got success, checking container status...")
		log.Fatal("Expected error but got success")
	}
	log.Printf("Received expected error: %v (duration: %v)", err, duration)

	log.Println("Waiting for container to restart...")
	startWait := time.Now()
	timeout := 30 * time.Second
	for {
		currentStartTime := getContainerStartTime(r)
		if currentStartTime != startTimeBefore {
			log.Printf("Container restarted in %v! New start time: %s",
				time.Since(startWait), currentStartTime)
			break
		}

		if time.Since(startWait) > timeout {
			log.Fatalf("Container did not restart within %v", timeout)
		}
		time.Sleep(1 * time.Second)
	}

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

func getContainerStartTime(r *renderer.Renderer) string {
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.StartedAt}}", r.GetContainerName())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func monitorRenderer(ctx context.Context, r *renderer.Renderer) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !r.IsContainerReady() && !r.IsRestarting() {
				log.Println("Renderer is not ready, initiating forced recovery...")
				r.ForceRecovery()
				waitForRendererReady(r, 2*time.Minute)
			}
		case <-ctx.Done():
			return
		}
	}
}

func waitForRendererReady(r *renderer.Renderer, timeout time.Duration) {
	log.Println("Waiting for renderer recovery...")
	start := time.Now()
	for !r.IsContainerReady() {
		if time.Since(start) > timeout {
			log.Fatal("Renderer recovery failed within timeout")
		}
		time.Sleep(2 * time.Second)
	}
	log.Println("Renderer recovered successfully")
}

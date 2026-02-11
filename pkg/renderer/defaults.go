package renderer

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"time"

	"github.com/chromedp/chromedp"
)

// DefaultLogger provides standard logging implementation
type DefaultLogger struct{}

func (l *DefaultLogger) log(prefix string, args ...interface{}) {
	log.Print("["+prefix+"] ", fmt.Sprint(args...))
}

func (l *DefaultLogger) logf(prefix, format string, args ...interface{}) {
	log.Printf("["+prefix+"] "+format, args...)
}

func (l *DefaultLogger) Info(args ...interface{}) {
	l.log("INFO", args...)
}

func (l *DefaultLogger) Infof(format string, args ...interface{}) {
	l.logf("INFO", format, args...)
}

func (l *DefaultLogger) Warn(args ...interface{}) {
	l.log("WARN", args...)
}

func (l *DefaultLogger) Warnf(format string, args ...interface{}) {
	l.logf("WARN", format, args...)
}

func (l *DefaultLogger) Error(args ...interface{}) {
	l.log("ERROR", args...)
}

func (l *DefaultLogger) Errorf(format string, args ...interface{}) {
	l.logf("ERROR", format, args...)
}

func (l *DefaultLogger) Debug(args ...interface{}) {
	l.log("DEBUG", args...)
}

func (l *DefaultLogger) Debugf(format string, args ...interface{}) {
	l.logf("DEBUG", format, args...)
}

// RealCommander executes system commands
type RealCommander struct{}

func (c *RealCommander) LookPath(file string) (string, error) {
	return exec.LookPath(file)
}

func (c *RealCommander) Command(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

// RealHTTPClient performs HTTP requests
type RealHTTPClient struct{}

func (c *RealHTTPClient) Do(req *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}

// RealAllocatorCreator manages Chrome connections
type RealAllocatorCreator struct{}

func (a *RealAllocatorCreator) CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc) {
	return chromedp.NewRemoteAllocator(ctx, url)
}

// RealPortChecker verifies port status
type RealPortChecker struct{}

func NewRealPortChecker() *RealPortChecker {
	return &RealPortChecker{}
}

func (c *RealPortChecker) IsPortAvailable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// RealCommandExecutor реализация для реального окружения
type RealCommandExecutor struct{}

func (r *RealCommandExecutor) RunCommand(name string, arg ...string) ([]byte, error) {
	cmd := exec.Command(name, arg...)
	return cmd.CombinedOutput()
}

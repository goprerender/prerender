package renderer

import (
	"context"
	"net/http"
	"os/exec"
	"time"
)

// Logger provides logging capabilities
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

// Commander executes system commands
type Commander interface {
	LookPath(file string) (string, error)
	Command(name string, arg ...string) *exec.Cmd
}

// HTTPClient performs HTTP requests
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// AllocatorCreator manages Chrome DevTools Protocol allocators
type AllocatorCreator interface {
	CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc)
}

// PortChecker verifies port availability
type PortChecker interface {
	IsPortAvailable(port int) bool
}

// PageRenderer renders web pages
type PageRenderer interface {
	RenderPage(url string, result *RenderResult) (string, error)
}

// ContainerManager controls container lifecycle
type ContainerManager interface {
	EnsureRunning() error
	GetStatus() string
	Restart() error
}

// RenderTimings tracks performance metrics
type RenderTimings struct {
	Navigation time.Duration // Time taken for navigation
	Waiting    time.Duration // Time spent waiting for page readiness
	Rendering  time.Duration // Time taken to render the page
	Total      time.Duration // Total time for rendering task
}

// RenderResult contains page rendering output
type RenderResult struct {
	HTML       string         // Rendered HTML content
	Console    []ConsoleEntry // Browser console messages
	Exception  string         // JavaScript exception if any
	TotalTime  time.Duration  // Total time for render operation
	RenderTime time.Duration  // Time taken to render page (deprecated, use Timings)
	Timings    RenderTimings  // Detailed timing information
}

// ConsoleEntry represents browser console message
type ConsoleEntry struct {
	Type     string   // Message type (log, error, warning, etc.)
	Messages []string // Message content
}

// CommandExecutor выполняет команды и возвращает их вывод
type CommandExecutor interface {
	RunCommand(name string, arg ...string) ([]byte, error)
}

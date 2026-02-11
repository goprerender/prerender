package renderer

import (
	"context"
	"net/http"
	"os/exec"

	"github.com/stretchr/testify/mock"
)

// CommandMock мокает вызовы команд
type CommandMock struct {
	mock.Mock
}

func (c *CommandMock) CombinedOutput() ([]byte, error) {
	args := c.Called()
	return args.Get(0).([]byte), args.Error(1)
}

// CommanderMock мокает интерфейс Commander
type CommanderMock struct {
	mock.Mock
}

func (c *CommanderMock) LookPath(file string) (string, error) {
	args := c.Called(file)
	return args.String(0), args.Error(1)
}

func (c *CommanderMock) Command(name string, arg ...string) *exec.Cmd {
	args := c.Called(name, arg)
	return args.Get(0).(*exec.Cmd)
}

// LoggerMock мокает интерфейс Logger
type LoggerMock struct {
	mock.Mock
}

func (l *LoggerMock) Info(args ...interface{}) {
	l.Called(args...)
}

func (l *LoggerMock) Infof(format string, args ...interface{}) {
	l.Called(append([]interface{}{format}, args...)...)
}

func (l *LoggerMock) Warn(args ...interface{}) {
	l.Called(args...)
}

func (l *LoggerMock) Warnf(format string, args ...interface{}) {
	l.Called(append([]interface{}{format}, args...)...)
}

func (l *LoggerMock) Error(args ...interface{}) {
	l.Called(args...)
}

func (l *LoggerMock) Errorf(format string, args ...interface{}) {
	l.Called(append([]interface{}{format}, args...)...)
}

func (l *LoggerMock) Debug(args ...interface{}) {
	l.Called(args...)
}

func (l *LoggerMock) Debugf(format string, args ...interface{}) {
	l.Called(append([]interface{}{format}, args...)...)
}

// HTTPClientMock мокает интерфейс HTTPClient
type HTTPClientMock struct {
	mock.Mock
}

func (h *HTTPClientMock) Do(req *http.Request) (*http.Response, error) {
	args := h.Called(req)
	resp, _ := args.Get(0).(*http.Response)
	return resp, args.Error(1)
}

// AllocatorCreatorMock мокает интерфейс AllocatorCreator
type AllocatorCreatorMock struct {
	mock.Mock
}

func (a *AllocatorCreatorMock) CreateRemoteAllocator(ctx context.Context, url string) (context.Context, context.CancelFunc) {
	args := a.Called(ctx, url)
	return args.Get(0).(context.Context), args.Get(1).(context.CancelFunc)
}

// PageRendererMock мокает интерфейс PageRenderer
type PageRendererMock struct {
	mock.Mock
}

func (p *PageRendererMock) RenderPage(url string, result *RenderResult) (string, error) {
	args := p.Called(url, result)
	return args.String(0), args.Error(1)
}

// PortCheckerMock мокает интерфейс PortChecker
type PortCheckerMock struct {
	mock.Mock
}

func (p *PortCheckerMock) IsPortAvailable(port int) bool {
	args := p.Called(port)
	return args.Bool(0)
}

type MockCommander struct {
	mock.Mock
	Commands []*exec.Cmd
}

func (m *MockCommander) LookPath(file string) (string, error) {
	args := m.Called(file)
	return args.String(0), args.Error(1)
}

func (m *MockCommander) Command(name string, arg ...string) *exec.Cmd {
	_ = m.Called(name, arg)
	cmd := &exec.Cmd{}
	m.Commands = append(m.Commands, cmd)
	return cmd
}

// ContainerManagerMock мокает интерфейс ContainerManager
type ContainerManagerMock struct {
	mock.Mock
}

func (m *ContainerManagerMock) EnsureRunning() error {
	args := m.Called()
	return args.Error(0)
}

func (m *ContainerManagerMock) GetStatus() string {
	args := m.Called()
	return args.String(0)
}

func (m *ContainerManagerMock) Restart() error {
	args := m.Called()
	return args.Error(0)
}

// Aliases for tests using MockXxx naming convention
type MockLogger = LoggerMock
type MockHTTPClient = HTTPClientMock
type MockAllocatorCreator = AllocatorCreatorMock
type MockPortChecker = PortCheckerMock
type MockContainerManager = ContainerManagerMock

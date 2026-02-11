package renderer

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockCommandExecutor для тестов
type MockCommandExecutor struct {
	mock.Mock
}

func (m *MockCommandExecutor) RunCommand(name string, arg ...string) ([]byte, error) {
	args := m.Called(name, arg)
	return args.Get(0).([]byte), args.Error(1)
}

func TestDockerContainerManager_GetStatus(t *testing.T) {
	logger := new(MockLogger)
	executor := new(MockCommandExecutor)

	testCases := []struct {
		name     string
		output   string
		err      error
		expected string
	}{
		{"Running", "running", nil, "running"},
		{"Exited", "exited", nil, "exited"},
		{"Error", "", errors.New("cmd error"), "unknown"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			executor.On("RunCommand",
				"docker",
				[]string{"inspect", "-f", "{{.State.Status}}", "test-container"},
			).Return([]byte(tc.output), tc.err).Once()

			manager := &DockerContainerManager{
				executor:      executor,
				containerName: "test-container",
				logger:        logger,
			}

			status := manager.GetStatus()
			assert.Equal(t, tc.expected, status)
			executor.AssertExpectations(t)
		})
	}
}

func TestDockerContainerManager_EnsureRunning(t *testing.T) {
	logger := new(MockLogger)
	executor := new(MockCommandExecutor)

	testCases := []struct {
		name          string
		statusOutput  string
		startOutput   string
		startErr      error
		expectedError bool
	}{
		{
			name:          "AlreadyRunning",
			statusOutput:  "running",
			expectedError: false,
		},
		{
			name:          "StartExitedContainer",
			statusOutput:  "exited",
			startOutput:   "",
			startErr:      nil,
			expectedError: false,
		},
		{
			name:          "StartCreatedContainer",
			statusOutput:  "created",
			startOutput:   "",
			startErr:      nil,
			expectedError: false,
		},
		{
			name:          "StartFailed",
			statusOutput:  "exited",
			startOutput:   "container start error",
			startErr:      errors.New("start failed"),
			expectedError: true,
		},
		{
			name:          "UnknownStatus",
			statusOutput:  "paused",
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Настройка вызова для получения статуса
			executor.On("RunCommand",
				"docker",
				[]string{"inspect", "-f", "{{.State.Status}}", "test-container"},
			).Return([]byte(tc.statusOutput), nil).Once()

			manager := &DockerContainerManager{
				executor:      executor,
				containerName: "test-container",
				logger:        logger,
			}

			// Ожидаемые вызовы для запуска контейнера
			if tc.statusOutput == "exited" || tc.statusOutput == "created" {
				logger.On("Infof", "Starting container %s...", "test-container").Once()
				executor.On("RunCommand",
					"docker", []string{"start", "test-container"},
				).Return([]byte(tc.startOutput), tc.startErr).Once()

				if tc.startErr == nil {
					logger.On("Infof", "Container %s started successfully", "test-container").Once()
				}
			} else if tc.statusOutput == "running" {
				logger.On("Infof", "Container %s is already running", "test-container").Once()
			} else {
				logger.On("Warnf", "Container %s has unexpected status: %s", "test-container", tc.statusOutput).Once()
			}

			err := manager.EnsureRunning()

			if tc.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			executor.AssertExpectations(t)
			logger.AssertExpectations(t)
		})
	}
}

func TestDockerContainerManager_Restart(t *testing.T) {
	logger := new(MockLogger)
	executor := new(MockCommandExecutor)

	t.Run("SuccessfulRestart", func(t *testing.T) {
		executor.On("RunCommand",
			"docker", []string{"inspect", "test-container"},
		).Return([]byte(""), nil).Once()

		executor.On("RunCommand",
			"docker", []string{"restart", "-t", "0", "test-container"},
		).Return([]byte(""), nil).Once()

		logger.On("Infof", "Restarting container %s...", "test-container").Once()
		logger.On("Infof", "Container %s restarted successfully", "test-container").Once()

		manager := &DockerContainerManager{
			executor:      executor,
			containerName: "test-container",
			logger:        logger,
		}

		err := manager.Restart()
		assert.NoError(t, err)
		executor.AssertExpectations(t)
		logger.AssertExpectations(t)
	})

	t.Run("ContainerNotExists", func(t *testing.T) {
		executor.On("RunCommand",
			"docker", []string{"inspect", "test-container"},
		).Return([]byte(""), errors.New("not found")).Once()

		logger.On("Infof", "Restarting container %s...", "test-container").Once()

		manager := &DockerContainerManager{
			executor:      executor,
			containerName: "test-container",
			logger:        logger,
		}

		err := manager.Restart()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "container does not exist")
		executor.AssertExpectations(t)
		logger.AssertExpectations(t)
	})

	t.Run("RestartFailed", func(t *testing.T) {
		executor.On("RunCommand",
			"docker", []string{"inspect", "test-container"},
		).Return([]byte(""), nil).Once()

		executor.On("RunCommand",
			"docker", []string{"restart", "-t", "0", "test-container"},
		).Return([]byte("restart failed"), errors.New("restart error")).Once()

		logger.On("Infof", "Restarting container %s...", "test-container").Once()

		manager := &DockerContainerManager{
			executor:      executor,
			containerName: "test-container",
			logger:        logger,
		}

		err := manager.Restart()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to restart container")
		executor.AssertExpectations(t)
		logger.AssertExpectations(t)
	})
}

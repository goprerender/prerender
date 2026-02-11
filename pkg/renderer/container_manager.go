package renderer

import (
	"fmt"
	"strings"
)

// DockerContainerManager manages Docker container operations
type DockerContainerManager struct {
	executor      CommandExecutor
	containerName string
	debugPort     int
	logger        Logger
}

// GetStatus retrieves current container state
func (d *DockerContainerManager) GetStatus() string {
	output, err := d.executor.RunCommand(
		"docker", "inspect", "-f", "{{.State.Status}}", d.containerName,
	)

	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

// EnsureRunning verifies and maintains container operation
func (d *DockerContainerManager) EnsureRunning() error {
	status := d.GetStatus()
	if status == "running" {
		d.logger.Infof("Container %s is already running", d.containerName)
		return nil
	}

	if status == "exited" || status == "created" {
		d.logger.Infof("Starting container %s...", d.containerName)
		output, err := d.executor.RunCommand("docker", "start", d.containerName)
		if err != nil {
			return fmt.Errorf("failed to start container: %v\n%s", err, output)
		}
		d.logger.Infof("Container %s started successfully", d.containerName)
		return nil
	}

	d.logger.Warnf("Container %s has unexpected status: %s", d.containerName, status)
	return fmt.Errorf("container status: %s", status)
}

// Restart terminates and relaunches container
func (d *DockerContainerManager) Restart() error {
	d.logger.Infof("Restarting container %s...", d.containerName)

	// Verify container exists
	_, err := d.executor.RunCommand("docker", "inspect", d.containerName)
	if err != nil {
		return fmt.Errorf("container does not exist: %v", err)
	}

	// Execute restart command
	output, err := d.executor.RunCommand("docker", "restart", "-t", "0", d.containerName)
	if err != nil {
		return fmt.Errorf("failed to restart container: %v\n%s", err, output)
	}
	d.logger.Infof("Container %s restarted successfully", d.containerName)
	return nil
}

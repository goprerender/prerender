// integration_test.go
package renderer_test

/*func TestDockerContainerLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// 1. Проверяем, что Docker установлен
	_, err := exec.LookPath("docker")
	require.NoError(t, err, "Docker not installed")

	// 2. Создаем рендерер
	logger := renderer.NewStdLogger()
	r := renderer.NewRenderer(logger)

	// 3. Проверяем статус контейнера
	status := r.GetContainerStatus()
	t.Logf("Initial container status: %s", status)

	// 4. Если контейнер работает - останавливаем
	if status == "running" {
		t.Log("Stopping container...")
		cmd := exec.Command("docker", "stop", renderer.ContainerName)
		require.NoError(t, cmd.Run())
		time.Sleep(2 * time.Second)
	}

	// 5. Запускаем контейнер через наш рендерер
	t.Log("Setting up container...")
	require.NoError(t, r.Setup())

	// 6. Проверяем, что контейнер запущен
	status = r.GetContainerStatus()
	assert.Equal(t, "running", status)

	// 7. Выполняем тестовый рендеринг
	t.Log("Performing test render...")
	result, err := r.DoRender("https://example.com")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.HTML, "Example Domain")

	// 8. Перезапускаем контейнер
	t.Log("Restarting container...")
	require.NoError(t, r.RestartContainer())

	// 9. Проверяем, что контейнер перезапущен
	status = r.GetContainerStatus()
	assert.Equal(t, "running", status)

	// 10. Выполняем еще один рендеринг
	t.Log("Performing test render after restart...")
	result, err = r.DoRender("https://example.com")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Contains(t, result.HTML, "Example Domain")

	// 11. Останавливаем контейнер
	t.Log("Stopping container...")
	cmd := exec.Command("docker", "stop", renderer.ContainerName)
	require.NoError(t, cmd.Run())
}

func TestRendererWithRealContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Настройка
	logger := renderer.NewStdLogger()
	r := renderer.NewRenderer(logger)
	require.NoError(t, r.Setup())

	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"Example Domain", "https://example.com", "Example Domain"},
		{"Google", "https://google.com", "Google"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := r.DoRender(tt.url)
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Contains(t, result.HTML, tt.expected)
		})
	}
}

func TestConcurrentRendering(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Настройка
	logger := renderer.NewStdLogger()
	r := renderer.NewRenderer(logger)
	require.NoError(t, r.Setup())

	// Запускаем 10 параллельных рендеров
	const workers = 10
	results := make(chan *renderer.RenderResult, workers)
	errors := make(chan error, workers)

	for i := 0; i < workers; i++ {
		go func() {
			result, err := r.DoRender("https://example.com")
			results <- result
			errors <- err
		}()
	}

	// Ожидаем завершения
	for i := 0; i < workers; i++ {
		err := <-errors
		result := <-results
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, result.HTML, "Example Domain")
	}
}*/

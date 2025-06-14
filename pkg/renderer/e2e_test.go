// e2e_test.go
package renderer_test

/*func TestE2ERenderWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end test in short mode")
	}

	// 1. Запускаем рендерер
	logger := renderer.NewStdLogger()
	rendererInstance := renderer.NewRenderer(logger)
	require.NoError(t, rendererInstance.Setup())

	// 2. Запускаем HTTP сервер
	srv := server.NewServer(rendererInstance, logger)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 3. Выполняем запрос на рендеринг
	client := &http.Client{}
	req, err := http.NewRequest("GET", ts.URL+"/render?url=https://example.com", nil)
	require.NoError(t, err)
	req.Header.Set("X-Prerender-Token", "test-token")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// 4. Проверяем ответ
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "text/html")
	// Здесь можно добавить парсинг HTML для проверки содержимого
}

func TestE2EHealthCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end test in short mode")
	}

	// 1. Запускаем рендерер
	logger := renderer.NewStdLogger()
	rendererInstance := renderer.NewRenderer(logger)
	require.NoError(t, rendererInstance.Setup())

	// 2. Запускаем HTTP сервер
	srv := server.NewServer(rendererInstance, logger)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 3. Выполняем health check
	resp, err := http.Get(ts.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	// 4. Проверяем ответ
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}*/

package healthcheck

import (
	"net/http"
	"net/http/httptest"
	"testing"

	routing "github.com/go-ozzo/ozzo-routing/v2"
	"github.com/stretchr/testify/assert"
)

func TestHealthCheck(t *testing.T) {
	// Create a new router
	router := routing.New()

	// Register healthcheck handler
	RegisterHandlers(router, "0.9.0")

	// Create a test request
	req, _ := http.NewRequest("GET", "/healthcheck", nil)
	w := httptest.NewRecorder()

	// Serve the request
	router.ServeHTTP(w, req)

	// Verify response
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "OK 0.9.0", w.Body.String())
}

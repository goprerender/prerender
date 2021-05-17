package healthcheck

import (
	"freicon/internal/test"
	"freicon/pkg/log"
	"net/http"
	"testing"
)

func TestAPI(t *testing.T) {
	logger, _ := log.NewForTest()
	router := test.MockRouter(logger)
	RegisterHandlers(router, "0.9.0")
	test.Endpoint(t, router, test.APITestCase{
		"ok", "GET", "/healthCheck", "", nil, http.StatusOK, `"OK 0.9.0"`,
	})
}

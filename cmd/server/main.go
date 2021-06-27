package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/chromedp/chromedp"
	"github.com/go-ozzo/ozzo-routing/v2"
	"github.com/go-ozzo/ozzo-routing/v2/access"
	"github.com/go-ozzo/ozzo-routing/v2/fault"
	"github.com/go-ozzo/ozzo-routing/v2/slash"
	"google.golang.org/grpc"
	"log"
	"net/http"
	"os"
	"prerender/internal/cachers"
	"prerender/internal/cachers/rstorage"
	"prerender/internal/healthcheck"
	"prerender/internal/renderer"
	"prerender/pkg/api/storage"
	prLog "prerender/pkg/log"
	"strings"
	"time"
)

const (
	address = "localhost:50051"
)

// Version indicates the current version of the application.
var Version = "1.0.0-beta.0"

var flagDebug = flag.Bool("debug", false, "debug level")

func main() {
	// create root logger tagged with server version
	logger := prLog.New(*flagDebug).With(nil, "PR Server", Version)

	// Set up a connection to the server.
	conn, err := grpc.Dial(address, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	sc := storage.NewStorageClient(conn)

	pc := rstorage.New(sc, logger)

	var allocator context.Context
	var cancelAlloc context.CancelFunc

	devToolWsUrl, err := renderer.GetDebugURL(logger)
	if err == nil {
		allocator, cancelAlloc = chromedp.NewRemoteAllocator(context.Background(), devToolWsUrl)
		defer cancelAlloc()
	} else {
		allocator = context.Background()
	}

	ctx, cancel := chromedp.NewContext(allocator)
	defer cancel()

	// build HTTP server
	address := fmt.Sprintf(":%v", "3000")
	hs := &http.Server{
		Addr:    address,
		Handler: buildHandler(ctx, pc, logger),
	}

	// start the HTTP server with graceful shutdown
	go routing.GracefulShutdown(hs, 10*time.Second, logger.Infof)
	logger.Infof("Prerender %v is running at %v", Version, address)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("ListenAndServe error: ", err)
		os.Exit(-1)
	}
}

func buildHandler(ctx context.Context, pc cachers.Сacher, logger prLog.Logger) *routing.Router {
	router := routing.New()

	router.Use(
		// all these handlers are shared by every route
		access.Logger(logger.Infof),
		slash.Remover(http.StatusMovedPermanently),
		fault.Recovery(logger.Infof),
	)

	healthcheck.RegisterHandlers(router, Version)

	router.Get("/render", handleRequest(ctx, pc, logger))

	return router
}

func handleRequest(ctx context.Context, pc cachers.Сacher, logger prLog.Logger) routing.Handler {
	return func(c *routing.Context) error {

		queryString := c.Request.URL.Query().Get("url")
		queryForce := c.Request.URL.Query().Get("force")

		force := false

		if queryForce == "true" || strings.Contains(queryString, "force=true") {
			logger.Warn("Force is true")
			force = true
		}

		newTabCtx, cancel := chromedp.NewContext(ctx)
		ctx, cancel := context.WithTimeout(newTabCtx, time.Minute)
		defer cancel()

		res, err := renderer.DoRender(ctx, queryString, pc, force, logger)
		if err != nil {
			return err
		}

		return c.Write(res)
	}
}

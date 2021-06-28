package main

import (
	"flag"
	"github.com/robfig/cron/v3"
	"google.golang.org/grpc"
	"log"
	"os"
	"os/signal"
	"prerender/internal/cachers/rstorage"
	"prerender/internal/executor"
	"prerender/internal/renderer"
	"prerender/internal/sitemap"
	"prerender/pkg/api/storage"
	prLog "prerender/pkg/log"
	"syscall"
)

const (
	address = "localhost:50051"
)

// Version indicates the current version of the application.
var Version = "1.0.0-beta.0"

var flagDebug = flag.Bool("debug", false, "debug level")
var flagForce = flag.Bool("force", true, "force refresh")

func main() {
	// create root logger tagged with server version
	logger := prLog.New(*flagDebug).With(nil, "PR Worker", Version)

	// Set up a connection to the server.
	conn, err := grpc.Dial(address, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	sc := storage.NewStorageClient(conn)

	pc := rstorage.New(sc, logger)

	r := renderer.NewRenderer(pc, logger)
	defer r.Cancel()

	e := executor.NewExecutor(r, pc, logger)

	pl := cron.VerbosePrintfLogger(log.New(os.Stdout, "cron: ", log.LstdFlags))

	c := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(pl)))

	startCroneRefresh(e, c, logger)

	var sm = func() {
		sitemap.BySitemap(e, *flagForce, logger)
		c.Start()
	}

	go sm()

	exit := make(chan os.Signal, 1)
	signal.Notify(exit, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	<-exit
	logger.Info("Service the server received a stop signal...")
}

func startCroneRefresh(e *executor.Executor, c *cron.Cron, logger prLog.Logger) {
	spec := "01 00 * * *"
	//spec := "*/1 * * * *"
	_, err := c.AddFunc(spec, func() {
		logger.Debug(spec)
		sitemap.BySitemap(e, true, logger)
	})
	if err != nil {
		panic(err)
	}
	logger.Info("Crone Refresh init Done")
}

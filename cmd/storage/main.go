package main

import (
	"context"
	"flag"
	"github.com/bluele/gcache"
	"github.com/goprerender/prerender/pkg/api/storage"
	"github.com/goprerender/prerender/pkg/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	port     = ":50051"
	duration = time.Hour * 24 * 7
)

var gc = gcache.New(100000).
	LRU().
	Build()

// server is used to implement Saver.
type server struct {
	storage.UnimplementedStorageServer
	logger log.Logger
}

// Store implements Saver
func (s *server) Store(ctx context.Context, in *storage.StoreRequest) (*storage.StoreReply, error) {
	//s.logger.Infof("Received: %v, %v", in.Page.GetData(), in.Page.GetHash())
	err := gc.SetWithExpire(in.Page.GetHash(), in.Page.GetData(), duration)
	if err != nil {
		return nil, status.Error(codes.Unknown, "")
	}
	return &storage.StoreReply{Api: "v1"}, nil
}

func (s *server) Get(ctx context.Context, in *storage.GetRequest) (*storage.GetReplay, error) {
	//s.logger.Infof("Received: %v", in.GetHash())
	value, err := gc.Get(in.Hash)
	if err != nil {
		return nil, status.Error(codes.NotFound, "not found")
	}
	return &storage.GetReplay{
		Data: value.([]byte),
	}, nil
}

func (s *server) Len(ctx context.Context, in *storage.LenRequest) (*storage.LenReplay, error) {
	//s.logger.Warn("Received: Len request")
	return &storage.LenReplay{Length: int32(gc.Len(true))}, nil
}

// Version indicates the current version of the application.
var Version = "1.0.0-beta.0"

var flagDebug = flag.Bool("debug", false, "debug level")

func main() {

	// create root logger tagged with server version
	logger := log.New(*flagDebug).With(nil, "PR Storage", Version)

	ctx := context.Background()
	lis, err := net.Listen("tcp", port)
	if err != nil {
		logger.Errorf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	storage.RegisterStorageServer(s, &server{logger: logger})

	// graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for range c {
			// sig is a ^C, handle it
			logger.Info("shutting down gRPC server...")

			s.GracefulStop()

			<-ctx.Done()
		}
	}()

	// start gRPC server
	logger.Info("starting gRPC server...")

	if err := s.Serve(lis); err != nil {
		logger.Errorf("failed to serve: %v", err)
		os.Exit(1)
	}
}

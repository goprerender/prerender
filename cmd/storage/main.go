package main

import (
	"context"
	"github.com/bluele/gcache"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"log"
	"net"
	"os"
	"os/signal"
	"prerender/pkg/api/storage"
	"syscall"
	"time"
)

const (
	port = ":50051"
	duration = time.Hour*24*7
)

var gc = gcache.New(100000).
		LRU().
		Build()

// server is used to implement Saver.
type server struct {
	storage.UnimplementedStorageServer
}

// Store implements Saver
func (s *server) Store(ctx context.Context, in *storage.StoreRequest) (*storage.StoreReply, error) {
	//log.Printf("Received: %v, %v", in.Page.GetData(), in.Page.GetHash())
	err := gc.SetWithExpire(in.Page.GetHash(), in.Page.GetData(), duration)
	if err != nil {
		return nil, status.Error(codes.Unknown, "")
	}
	return &storage.StoreReply{Api: "v1"}, nil
}

func (s *server) Get(ctx context.Context, in *storage.GetRequest) (*storage.GetReplay, error) {
	//log.Printf("Received: %v", in.GetHash())
	value, err := gc.Get(in.Hash)
	if err != nil {
		return nil, status.Error(codes.NotFound, "not found")
	}
	return &storage.GetReplay{
		Data: value.([]byte),
	}, nil
}

func (s *server) Len(ctx context.Context, in *storage.LenRequest) (*storage.LenReplay, error) {
	log.Printf("Received: Len request")
	return &storage.LenReplay{Length: int32(gc.Len(true))}, nil
}

func main() {

	ctx := context.Background()
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	storage.RegisterStorageServer(s, &server{})

	// graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for range c {
			// sig is a ^C, handle it
			log.Println("shutting down gRPC server...")

			s.GracefulStop()

			<-ctx.Done()
		}
	}()

	// start gRPC server
	log.Println("starting gRPC server...")

	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
		os.Exit(1)
	}
}

package rstorage

import (
	"context"
	"github.com/golang/protobuf/ptypes"
	"log"
	"prerender/internal/cachers"
	"prerender/pkg/api/storage"
	"time"
)

type server struct {
	gw storage.StorageClient
}

func New(gw storage.StorageClient) cachers.Сacher {
	return server{gw: gw}
}

func (s server) Put(key string, data []byte) error {
	ctx := context.Background()
	now, _ := ptypes.TimestampProto(time.Now())
	req := storage.StoreRequest{Api: "v1", Page: &storage.Page{
		Hash:      key,
		Data:      data,
		CreatedAt: now,
	}}
	_, err := s.gw.Store(ctx, &req)
	if err != nil {
		return err
	}
	return err
}

func (s server) Get(key string) ([]byte, error) {
	ctx := context.Background()
	req := storage.GetRequest{Hash: key}
	result, err := s.gw.Get(ctx, &req)
	if err != nil {
		return []byte{}, err
	}
	return result.GetData(), err
}

func (s server) Len() int {
	ctx := context.Background()
	req := storage.LenRequest{}
	result, err := s.gw.Len(ctx, &req)
	if err != nil {
		log.Fatal( err)
	}
	return int(result.GetLength())
}

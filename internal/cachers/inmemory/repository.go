package inmemory

import (
	"github.com/bluele/gcache"
	"prerender/internal/cachers"
	"time"
)

type repository struct {
	gc gcache.Cache
	dd time.Duration
}

func New(gc gcache.Cache) cachers.Сacher {
	return repository{gc: gc, dd: time.Hour*24*7}
}

func (r repository) Put(key string, data []byte) error {
	return r.gc.SetWithExpire(key, data, r.dd)
}

func (r repository) Get(key string) ([]byte, error) {
	value, err := r.gc.Get(key)
	if err != nil {
		return []byte{}, err
	}
	return value.([]byte), nil
}

func (r repository) Len() int {
	return r.gc.Len(true)
}

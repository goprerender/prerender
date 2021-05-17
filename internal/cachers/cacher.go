package cachers

type Сacher interface {
	Put(key string, data []byte) error
	Get(key string) ([]byte, error)
	Len() int
 }

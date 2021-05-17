package archive

import (
	"bytes"
	"compress/gzip"
	"io"
	"prerender/pkg/log"
	"strings"
	"time"
)

func GzipHtml(s, name, comment string, logger log.Logger) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)

	// Setting the Header fields is optional.
	zw.Name = name
	zw.Comment = comment
	zw.ModTime = time.Now()

	_, err := zw.Write([]byte(s))
	if err != nil {
		logger.Error(err)
	}

	if err := zw.Close(); err != nil {
		logger.Error(err)
	}

	return buf.Bytes()
}

func UnzipHtml(b []byte, logger log.Logger) string {
	buf := bytes.NewBuffer(b)
	zr, err := gzip.NewReader(buf)
	if err != nil {
		logger.Error(err)
	}

	out := new(strings.Builder)

	if _, err := io.Copy(out, zr); err != nil {
		logger.Error(err)
	}

	if err := zr.Close(); err != nil {
		logger.Error(err)
	}
	return out.String()
}

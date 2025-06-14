package archive

import (
	"bytes"
	"compress/gzip"
	"github.com/goprerender/prerender/pkg/log"
	"io"
	"strings"
	"time"
)

func GzipHtml(s, name, comment string, logger log.Logger) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)

	// Устанавливаем метаданные для архива
	zw.Name = name
	zw.Comment = comment
	zw.ModTime = time.Now()

	// Записываем данные
	if _, err := zw.Write([]byte(s)); err != nil {
		logger.Errorf("Gzip write error: %v", err)
		return nil, err
	}

	// Закрываем writer для завершения архивации
	if err := zw.Close(); err != nil {
		logger.Errorf("Gzip close error: %v", err)
		return nil, err
	}

	return buf.Bytes(), nil
}

func UnzipHtml(b []byte, logger log.Logger) (string, error) {
	buf := bytes.NewBuffer(b)
	zr, err := gzip.NewReader(buf)
	if err != nil {
		logger.Errorf("Gzip reader error: %v", err)
		return "", err
	}
	defer zr.Close()

	out := new(strings.Builder)

	// Копируем данные из reader в buffer
	if _, err := io.Copy(out, zr); err != nil {
		logger.Errorf("Gzip copy error: %v", err)
		return "", err
	}

	// Возвращаем распакованную строку
	return out.String(), nil
}

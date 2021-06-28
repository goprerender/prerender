package executor

import (
	"crypto/sha256"
	"fmt"
	"prerender/internal/archive"
	"prerender/internal/cachers"
	"prerender/internal/helper"
	"prerender/internal/renderer"
	"prerender/pkg/log"
)

type Executor struct {
	renderer *renderer.Renderer
	pc       cachers.Сacher
	logger   log.Logger
}

func NewExecutor(renderer *renderer.Renderer, c cachers.Сacher, logger log.Logger) *Executor {
	return &Executor{
		renderer: renderer,
		pc:       c,
		logger:   logger,
	}
}

func (e *Executor) GetPC() cachers.Сacher {
	return e.pc
}

func (e *Executor) Execute(url string, force bool) (string, error) {
	var res string

	hostPath, requestURL := helper.Parse(url, e.logger)

	key := fmt.Sprintf("%x", sha256.Sum256([]byte(hostPath)))
	value, err := e.pc.Get(key)
	if force || err != nil {

		res, err := e.renderer.DoRender(requestURL)
		if err != nil {
			return res, err
		}

		htmlGzip := archive.GzipHtml(res, hostPath, "", e.logger)
		err4 := e.pc.Put(key, htmlGzip)
		if err4 != nil {
			e.logger.Warn("Can't store result in cache")
		}
	} else {
		res = archive.UnzipHtml(value, e.logger)
	}
	return res, nil
}

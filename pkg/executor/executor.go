package executor

import (
	"crypto/sha256"
	"fmt"
	"github.com/goprerender/prerender/internal/cachers"
	"github.com/goprerender/prerender/pkg/archive"
	"github.com/goprerender/prerender/pkg/log"
	"github.com/goprerender/prerender/pkg/renderer"
	"github.com/goprerender/prerender/pkg/url"
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

func (e *Executor) Execute(query string, force bool) (string, error) {
	var res string

	hostPath, err := url.SlashRemover(query, e.logger)
	if err != nil {

		return hostPath, err
	}

	key := fmt.Sprintf("%x", sha256.Sum256([]byte(hostPath)))
	//e.logger.Infof("hostPath: %s, query: %s", hostPath, query)
	value, err := e.pc.Get(key)
	if force || err != nil {
		/*start:
		if e.renderer.IsRestarting() {
			time.Sleep(time.Second)
			goto start
		}*/
		res, err = e.renderer.DoRender(query)
		if err != nil {
			return res, err
		}

		//e.logger.Infof("html: %s", res)

		htmlGzip := archive.GzipHtml(res, hostPath, "", e.logger)
		err = e.pc.Put(key, htmlGzip)
		if err != nil {
			e.logger.Warn("Can't store result in cache")
		}
	} else {
		res = archive.UnzipHtml(value, e.logger)
	}
	return res, nil
}

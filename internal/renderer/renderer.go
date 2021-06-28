package renderer

import (
	"context"
	"encoding/json"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"net/http"
	"prerender/internal/cachers"
	"prerender/pkg/log"
	"time"
)

type Renderer struct {
	allocatorCtx context.Context
	cancel       context.CancelFunc
	pc           cachers.Сacher
	logger       log.Logger
}

func NewRenderer(pc cachers.Сacher, logger log.Logger) *Renderer {
	r := &Renderer{
		pc:     pc,
		logger: logger,
	}
	r.Setup()
	return r
}

func (r *Renderer) DoRender(requestURL string) (string, error) {

	newTabCtx, cancel := chromedp.NewContext(r.allocatorCtx)
	defer cancel()

	ctx, cancel := context.WithTimeout(newTabCtx, time.Second*120)
	defer cancel()

	var attempts = 0
	var restart = false

	var res string

start:
	err := chromedp.Run(ctx,
		network.SetBlockedURLS([]string{"google-analytics.com", "mc.yandex.ru"}),
		chromedp.Navigate(requestURL),
		//chromedp.Sleep(time.Second*3), // ToDo add dynamics sleep timeout
		chromedp.ActionFunc(func(ctx context.Context) error {
			node, err := dom.GetDocument().Do(ctx)
			if err != nil {
				r.logger.Error("GetDocument: ", err)
				return err
			}
			res, err = dom.GetOuterHTML().WithNodeID(node.NodeID).Do(ctx)
			return err
		}),
	)

	if err != nil {
		r.logger.Error("ChromeDP error: ", err, ", url:", requestURL)
		if attempts <= 5 {
			r.logger.Error("ChromeDP sleep for 15 sec ", attempts)
			time.Sleep(15 * time.Second)
			attempts++
			r.logger.Error("ChromeDP trying attempt: ", attempts)
			goto start
		}
		if attempts == 0 && !restart {
			r.cancel()
			r.Setup()
			attempts = 0
			r.logger.Warn("Reset attempts... ", attempts)
			restart = true
			r.logger.Error("ChromeDP trying to restart Chrome...")
			goto start
		}

		return "", err
	}

	return res, nil
}

func (r *Renderer) Setup() {
	devToolWsUrl, err := GetDebugURL(r.logger)
	if err == nil {
		r.allocatorCtx, r.cancel = chromedp.NewRemoteAllocator(context.Background(), devToolWsUrl)
	} else {
		r.allocatorCtx = context.Background()
	}
}

func (r *Renderer) Cancel() {
	r.cancel()
}

func GetDebugURL(logger log.Logger) (string, error) {
	resp, err := http.Get("http://localhost:9222/json/version")
	if err != nil {
		logger.Warn("Get Debug URL: ", err)
		return "", err
	}

	var result map[string]interface{}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logger.Error("Decoder Debug URL: ", err)
		return "", err
	}
	return result["webSocketDebuggerUrl"].(string), nil
}

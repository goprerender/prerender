package renderer

import (
	"context"
	"encoding/json"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"net/http"
	"os/exec"
	"prerender/internal/cachers"
	"prerender/pkg/log"
	"sync"
	"time"
)

type Renderer struct {
	allocatorCtx context.Context
	cancel       context.CancelFunc
	pc           cachers.Сacher
	isRestarting bool
	mutex        sync.Mutex
	logger       log.Logger
}

func (r *Renderer) IsRestarting() bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.isRestarting
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
start:
	newTabCtx, cancel := chromedp.NewContext(r.allocatorCtx)
	defer cancel()

	ctx, cancel := context.WithTimeout(newTabCtx, time.Second*120)
	defer cancel()

	var attempts = 0

	var res string

next:
	headers := network.Headers{"X-Prerender-Next": "1"}

	err := chromedp.Run(ctx,
		network.SetBlockedURLS([]string{"google-analytics.com", "mc.yandex.ru"}),
		network.SetExtraHTTPHeaders(headers),
		chromedp.Navigate(requestURL),
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
		if attempts < 3 {
			attempts++
			r.logger.Warn("ChromeDP sleep for 5 sec, att: ", attempts)
			time.Sleep(1 * time.Second)
			goto next
		}
		if attempts >= 3 && !r.IsRestarting() {
			r.logger.Warn("Closing Chrome.. ", attempts)
			r.cancel()
			r.logger.Warn("Restarting headless-shell container... ", attempts)
			err := r.rebootContainer()
			if err != nil {
				return "", err
			}
			time.Sleep(1 * time.Second)
			r.logger.Warn("Starting Chrome.. ", attempts)
			r.Setup()
			attempts = 0
			r.logger.Warn("Waiting for restart Chrome, sleep 1 sec...")
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
		r.logger.Warn("Trying to connect to local chrome")
		r.allocatorCtx, r.cancel = context.WithCancel(context.Background())
	}
}

func (r *Renderer) Cancel() {
	r.cancel()
}

func GetDebugURL(logger log.Logger) (string, error) {
	resp, err := http.Get("http://localhost:9222/json/version")
	if err != nil {
		logger.Warn("Error get debug URL: ", err)
		return "", err
	}

	var result map[string]interface{}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logger.Error("Error decode json for debug URL: ", err)
		return "", err
	}

	debugUrl := result["webSocketDebuggerUrl"].(string)
	logger.Info("Debug URL: ", debugUrl)

	return debugUrl, nil
}

func (r *Renderer) rebootContainer() error {
	r.mutex.Lock()
	defer func() {
		r.isRestarting = false
		r.mutex.Unlock()
	}()

	r.isRestarting = true

	container := "headless-shell"

	path, err := exec.LookPath("docker")
	if err != nil {
		r.logger.Errorf("installing docker is in your future")
		return err
	}
	r.logger.Infof("docker is available at %s\n", path)

	out, err := exec.Command("docker", "restart", container).Output()
	if err != nil {
		r.logger.Error(err)
		return err
	}

	outResult := string(out)
	r.logger.Info(outResult)
	if outResult != container {
		r.logger.Errorf("Not a good answer from docker...")
		return err
	}
	
	time.Sleep(5 * time.Second)

	return nil
}

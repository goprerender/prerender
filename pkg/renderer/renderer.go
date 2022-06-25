package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/goprerender/prerender/pkg/log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Renderer struct {
	allocatorCtx context.Context
	cancel       context.CancelFunc
	isRemote     bool
	isStarted    bool
	isRestarting bool
	dockerPath   string
	lastStart    time.Time
	mutex        sync.Mutex
	logger       log.Logger
}

func (r *Renderer) IsRestarting() bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.isRestarting
}

func NewRenderer(logger log.Logger) *Renderer {
	r := &Renderer{
		logger: logger,
	}
	r.Setup()
	return r
}

var ErrNotResponding = errors.New("error: Chrome not responding")

func (r *Renderer) DoRender(requestURL string) (string, error) {
	var res string
	var attempts = 0

	startTime := time.Now()

start:
	if r.IsRestarting() { //ToDo try to use callback or https://github.com/ReactiveX/RxGo
		r.logger.Warn("Docker container is restarting... sleep 5 sec and try again ", attempts)
		time.Sleep(5 * time.Second)
		attempts++
		if attempts > 5 {
			return res, ErrNotResponding
		}
		goto start
	}

	//Open new tab
	newTabCtx, cancel := chromedp.NewContext(r.allocatorCtx)
	defer cancel()

	//new context with timeout
	ctx, cancel := context.WithTimeout(newTabCtx, time.Second*60)
	defer cancel()

next:
	headers := network.Headers{"X-Prerender-Next": "1"}

	r.logger.Debugf("Request url: %s", requestURL)

	err := chromedp.Run(ctx,
		network.SetBlockedURLS([]string{
			"google-analytics.com",
			"mc.yandex.ru",
			"maps.googleapis.com",
			"googletagmanager.com",
			"api-maps.yandex.ru",
		}),
		network.SetExtraHTTPHeaders(headers),
		//network.SetBypassServiceWorker(true),
		//network.SetCacheDisabled(true),
		//chromedp.Sleep(5*time.Second),
		chromedp.Navigate(requestURL),
		//chromedp.WaitReady("body"),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.OuterHTML("html", &res, chromedp.ByQuery),
	)

	//time.Sleep(10 * time.Second)

	endTime := time.Now()

	delta := endTime.Sub(startTime).Seconds()
	r.logger.Debugf("Duration: %f seconds", delta)

	if err != nil {
		r.logger.Error("ChromeDP error: ", err, ", url:", requestURL)

		if strings.HasPrefix(err.Error(), "could not dial \"ws:") {
			cancel()

			attempts++

			if attempts >= 3 && !r.IsRestarting() {
				r.logger.Warn("Try to re setup Chrome...")
				err := r.Restart()
				if err != nil {
					r.logger.Warn("Error restarting container...")
					return "", err
				}
				r.logger.Warn("Chrome setup complete...")
				attempts = 0
			}

			time.Sleep(1 * time.Second)
			goto start
		}

		if strings.HasPrefix(err.Error(), "Could not find node with given id") {
			cancel()
			goto start
		}

		if strings.HasPrefix(err.Error(), "exec: \"google-chrome\": executable file not found in") {
			r.isStarted = false
			r.Setup()
			goto start
		}

		if attempts < 3 {
			attempts++
			r.logger.Warn("ChromeDP sleep for 1 sec, att: ", attempts)

			time.Sleep(1 * time.Second)

			if err == context.DeadlineExceeded {
				cancel()
				time.Sleep(3 * time.Second)
				goto start
			}
			if err == context.Canceled {
				cancel()
				time.Sleep(3 * time.Second)
				goto start
			}
			goto next
		}

		return "", err
	}

	return res, nil
}

func (r *Renderer) Setup() {
	if !r.isStarted {
		if r.IsRestarting() {
			return
		}
		err := r.setupContainer()
		if err != nil {
			r.logger.Warnf("Container not setup properly or not available")
		}
	}

	r.logger.Infof("Try to setup Chrome")
	devToolWsUrl, err := GetDebugURL(r.logger)

	if err == nil {
		r.allocatorCtx, r.cancel = chromedp.NewRemoteAllocator(context.Background(), devToolWsUrl)
		r.isRemote = true
	} else {
		r.logger.Warn("Trying to connect to local chrome")
		r.allocatorCtx, r.cancel = context.WithCancel(context.Background())
		r.isRemote = false
	}
}

func (r *Renderer) Restart() error {
	if r.isRemote {
		if r.IsRestarting() {
			err := r.rebootContainer()
			if err != nil {
				return err
			}
		}
	}
	r.Setup()
	return nil
}

func (r *Renderer) Cancel() {
	r.cancel()
}

func GetDebugURL(logger log.Logger) (string, error) {
	logger.Infof("Try to get data from remote Chrome...")
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

const container = "headless-shell"

func (r *Renderer) setupContainer() error {
	r.mutex.Lock()
	defer func() {
		r.isRestarting = false
		r.mutex.Unlock()
	}()

	path, err := r.checkDocker()
	if err != nil {
		return err
	}

	r.dockerPath = path

	err = r.checkImage()
	if err != nil {
		return err
	}

	r.lastStart = time.Now()

	r.isStarted = true

	//time.Sleep(1 * time.Second)

	return nil
}

func (r *Renderer) rebootContainer() error {
	r.mutex.Lock()
	defer func() {
		r.isRestarting = false
		r.mutex.Unlock()
	}()

	if time.Now().Sub(r.lastStart) < 3*time.Minute {
		r.logger.Warnf("Docker was restarted less than 3 minutes, now sleep 5 sec and exiting...")
		time.Sleep(5 * time.Second)
		return nil
	}

	r.isRestarting = true

	out, err := exec.Command(r.dockerPath, "restart", container).Output()
	if err != nil {
		r.logger.Error(err)
		return err
	}

	outResult := string(out)
	r.logger.Infof("outFromCmd: %s, cont: %s", outResult, container)
	if !strings.Contains(outResult, container) {
		r.logger.Errorf("Not a good answer from docker...")
		return err
	}
	r.lastStart = time.Now()

	r.isStarted = true

	//time.Sleep(1 * time.Second)

	return nil
}

func (r *Renderer) checkDocker() (string, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		r.logger.Errorf("installing docker is in your future")
		return "", err
	}
	r.logger.Infof("docker is available at %s\n", path)
	return path, nil
}

func (r *Renderer) checkImage() error {
	out, err := exec.Command(r.dockerPath, "ps", "-a").Output()
	if err != nil {
		r.logger.Error(err)
		return err
	}

	outResult := string(out)
	r.logger.Infof("outFromCmd: %s, cont: %s", outResult, container)
	if !strings.Contains(outResult, container) {
		r.logger.Errorf("Not a good answer from docker...")
		//return err
	}
	if !strings.Contains(outResult, "Up") {
		r.logger.Errorf("Image not working...")
		out, err := exec.Command("docker", "restart", container).Output()
		if err != nil {
			r.logger.Error(err)
			return err
		}
		outResult = string(out)
		if !strings.Contains(outResult, container) {
			r.logger.Errorf("Not a good answer from docker...")
			//return err
		}

		out, err = exec.Command("docker", "ps", "-a", container).Output()
		if err != nil {
			r.logger.Error(err)
			return err
		}
		if !strings.Contains(outResult, "Up") {
			r.logger.Errorf("Image not working still...")
		}
	}

	r.lastStart = time.Now()

	r.isStarted = true

	return nil
}

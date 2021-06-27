package renderer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"net/http"
	"net/url"
	"prerender/internal/archive"
	"prerender/internal/cachers"
	"prerender/pkg/log"
	"strings"
)

func DoRender(ctx context.Context, queryString string, pc cachers.Сacher, force bool, logger log.Logger) (string, error) {
	u, err := url.Parse(queryString)
	if err != nil {
		logger.Error("Pars URL: ", err)
	}

	requestURL := ""
	hostPath := ""

	if u.Path != "/" && strings.HasSuffix(u.Path, "/") {
		path := strings.TrimRight(u.Path, "/")
		requestURL = u.Scheme + "://" + u.Host + path
		hostPath = u.Host + path
	} else {
		requestURL = queryString
		hostPath = u.Host + u.Path
	}

	var res string
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(hostPath)))
	value, err := pc.Get(key)
	if force || err != nil {
		err1 := chromedp.Run(ctx,
			network.SetBlockedURLS([]string{"google-analytics.com", "mc.yandex.ru"}),
			chromedp.Navigate(requestURL),
			//chromedp.Sleep(time.Second*60), // ToDo add dynamics sleep timeout
			chromedp.ActionFunc(func(ctx context.Context) error {
				node, err2 := dom.GetDocument().Do(ctx)
				if err2 != nil {
					logger.Error("GetDocument: ", err2)
					return err2
				}
				res, err2 = dom.GetOuterHTML().WithNodeID(node.NodeID).Do(ctx)
				return err2
			}),
		)

		if err1 != nil {
			logger.Error("ChromeDP error: ", err1)
			return "", err1
		}
		htmlGzip := archive.GzipHtml(res, hostPath, "", logger)
		err4 := pc.Put(key, htmlGzip)
		if err4 != nil {
			logger.Warn("Can't store result in cache")
		}
	} else {
		res = archive.UnzipHtml(value, logger)
	}

	return res, nil
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

package renderer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/chromedp/cdproto/dom"
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
		logger.Error(err)
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
		err := chromedp.Run(ctx,
			chromedp.Navigate(requestURL),
			chromedp.ActionFunc(func(ctx context.Context) error {
				node, err := dom.GetDocument().Do(ctx)
				if err != nil {
					return err
				}
				res, err = dom.GetOuterHTML().WithNodeID(node.NodeID).Do(ctx)
				return err
			}),
		)

		if err != nil {
			fmt.Println(err)
			return "", err
		}
		htmlGzip := archive.GzipHtml(res, hostPath, "", logger)
		err = pc.Put(key, htmlGzip)
		if err != nil {
			return "", err
		}
	} else {
		res = archive.UnzipHtml(value, logger)
	}

	return res, nil
}

func GetDebugURL(logger log.Logger) (string, error) {
	resp, err := http.Get("http://localhost:9222/json/version")
	if err != nil {
		logger.Warn(err)
		return "", err
	}

	var result map[string]interface{}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		logger.Error(err)
		return "", err
	}
	return result["webSocketDebuggerUrl"].(string), nil
}


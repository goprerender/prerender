package sitemap

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/chromedp/chromedp"
	"github.com/yterajima/go-sitemap"
	"io/ioutil"
	"prerender/internal/cachers"
	"prerender/internal/renderer"
	"prerender/pkg/log"
	"time"
)

func BySitemap(ctx context.Context, pc cachers.Сacher, force bool, logger log.Logger) {
	type sitemaps []string

	f, err := ioutil.ReadFile("sitemaps.json")
	if err != nil {
		logger.Errorf("Couldn't load sitemap file")
	}

	var sitemapUrls sitemaps

	err = json.Unmarshal(f, &sitemapUrls)
	if err != nil {
		logger.Errorf("Error parsing sitemap.json")
	}

	for _, j := range sitemapUrls {
		go doSitemap(ctx, pc, force, j, logger)
	}
}

func doSitemap(ctx context.Context, pc cachers.Сacher, force bool, sitemapUrl string, logger log.Logger) {
	siteMap, err := sitemap.Get(sitemapUrl, nil)
	if err != nil {
		fmt.Println(err)
	}

	logger.Info("Sitemap len: ", len(siteMap.URL))

	for _, URL := range siteMap.URL {
		logger.Info(URL.Loc)
		newTabCtx, cancel := chromedp.NewContext(ctx)
		ctx, cancel := context.WithTimeout(newTabCtx, time.Second*30)

		_, err := renderer.DoRender(ctx, URL.Loc, pc, force, logger)
		if err != nil {
			logger.Error(err)
			continue
		}
		cancel()
	}

	logger.Infof("Finished %s, Cache len: %d", sitemapUrl, pc.Len())
}

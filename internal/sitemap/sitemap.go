package sitemap

import (
	"encoding/json"
	"github.com/goprerender/prerender/pkg/executor"
	"github.com/goprerender/prerender/pkg/log"
	"github.com/yterajima/go-sitemap"
	"io/ioutil"
)

func BySitemap(r *executor.Executor, force bool, logger log.Logger) {
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
		doSitemap(r, force, j, logger)
	}
}

func doSitemap(e *executor.Executor, force bool, sitemapUrl string, logger log.Logger) {
	siteMap, err := sitemap.Get(sitemapUrl, nil)
	if err != nil {
		logger.Error("Get Sitemap: ", err)
	}

	logger.Infof("Sitemap len: %d", len(siteMap.URL))

	for _, URL := range siteMap.URL {
		logger.Info("SM URL: ", URL.Loc)

		_, err := e.Execute(URL.Loc, force)
		if err != nil {
			logger.Error("Sitemap Renderer error: ", err, ", url: ", URL.Loc)
			continue
		}
	}

	logger.Infof("Finished %s, Cache len: %d", sitemapUrl, e.GetPC().Len())
}

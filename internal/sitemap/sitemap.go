package sitemap

import (
	"encoding/json"
	"github.com/yterajima/go-sitemap"
	"io/ioutil"
	"prerender/internal/renderer"
	"prerender/pkg/log"
)

func BySitemap(r *renderer.Renderer, force bool, logger log.Logger) {
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
		go doSitemap(r, force, j, logger)
	}
}

func doSitemap(r *renderer.Renderer, force bool, sitemapUrl string, logger log.Logger) {
	siteMap, err := sitemap.Get(sitemapUrl, nil)
	if err != nil {
		logger.Error("Get Sitemap: ", err)
	}

	logger.Infof("Sitemap len: %d", len(siteMap.URL))

	for _, URL := range siteMap.URL {
		logger.Info("SM URL: ", URL.Loc)

		_, err := r.DoRender(URL.Loc, force)
		if err != nil {
			logger.Error("Sitemap Renderer error: ", err)
			continue
		}
	}

	logger.Infof("Finished %s, Cache len: %d", sitemapUrl, r.GetCacher().Len())
}

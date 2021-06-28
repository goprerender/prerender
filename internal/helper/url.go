package helper

import (
	"net/url"
	"prerender/pkg/log"
	"strings"
)

func Parse(queryString string, logger log.Logger) (string, string) {
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
	return hostPath, requestURL
}

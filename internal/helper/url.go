package helper

import (
	"errors"
	"net/url"
	"prerender/pkg/log"
	"strings"
)

var ErrRedirect = errors.New("err: trailing slash, need redirection")

func Parse(queryString string, logger log.Logger) (string, error) {
	u, err := url.Parse(queryString)
	if err != nil {
		logger.Error("Pars URL: ", err)
	}

	hostPath := ""

	if u.Path != "/" && strings.HasSuffix(u.Path, "/") {
		return hostPath, ErrRedirect
	}

	hostPath = u.Host + u.Path + u.RawQuery

	return hostPath, nil
}

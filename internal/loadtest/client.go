package loadtest

import (
	"net/http"
	"time"
)

func newHTTPClient(conns int) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = conns
	tr.MaxIdleConnsPerHost = conns
	tr.MaxConnsPerHost = 0
	tr.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}
}

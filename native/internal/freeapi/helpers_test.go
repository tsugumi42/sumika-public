package freeapi

import (
	"net/http"
	"net/http/httptest"
)

func newServer(h func(http.ResponseWriter, *http.Request)) *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(h))
}

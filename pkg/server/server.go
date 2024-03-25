package server

import (
	"net/http/httputil"
	"net/url"
	"sync"
)

type Backend struct {
	Url    *url.URL
	Active bool
	mu     sync.RWMutex
	Proxy  *httputil.ReverseProxy
}

func (b *Backend) SetActive(active bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Active = active
}

func (b *Backend) IsActive() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Active
}

package serverpool

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/shameerb/tiny-lb/pkg/server"
)

type Key int

const (
	Attempts Key = iota
	Retry
)

type ServerPool struct {
	backends   []*server.Backend
	currentIdx uint64
}

func (s *ServerPool) markBackend(url *url.URL, status bool) {
	for _, b := range s.backends {
		if b.Url.String() == url.String() {
			b.SetActive(status)
			return
		}
	}
}

func (s *ServerPool) Lb(w http.ResponseWriter, r *http.Request) {
	nxt := s.GetNextServer()
	if nxt == nil {
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	nxt.Proxy.ServeHTTP(w, r)
}

func (s *ServerPool) AddServer(endpoint string) {
	url, err := url.Parse(endpoint)
	if err != nil {
		log.Fatal(err.Error())
	}
	proxy := httputil.NewSingleHostReverseProxy(url)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		// check the retries count
		var retries int
		var ok bool
		retries, ok = r.Context().Value(Retry).(int)
		if !ok {
			retries = 1
		}
		if retries < 3 {
			<-time.After(10 * time.Millisecond)
			ctx := context.WithValue(r.Context(), Retry, retries+1)
			proxy.ServeHTTP(w, r.WithContext(ctx))
		}
		s.markBackend(url, false)
		// attempts are basically hitting against another server. retry is using the same backend server 3 times.
		var attempts int
		attempts, ok = r.Context().Value(Attempts).(int)
		if !ok {
			attempts = 1
		}
		if attempts > 3 {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx := context.WithValue(r.Context(), Attempts, attempts+1)
		s.Lb(w, r.WithContext(ctx))
		// check the nos of attempts
	}

	s.backends = append(s.backends, &server.Backend{
		Url:    url,
		Active: true,
		Proxy:  proxy,
	})
}

func (s *ServerPool) NextIndex() int {
	return int(atomic.AddUint64(&s.currentIdx, 1)) % len(s.backends)
}

func (s *ServerPool) GetNextServer() *server.Backend {
	nxt := s.NextIndex()
	for i := nxt; i < len(s.backends)+nxt; i++ {
		idx := i % len(s.backends)
		if s.backends[idx].IsActive() {
			if idx != int(s.currentIdx) {
				atomic.StoreUint64(&s.currentIdx, uint64(idx))
			}
			return s.backends[idx]
		}
	}
	return nil
}

func (s *ServerPool) HealthCheck() {
	for _, backend := range s.backends {
		conn, err := net.Dial("tcp", backend.Url.Host)
		if err != nil {
			backend.SetActive(false)
			return
		}
		backend.SetActive(true)
		conn.Close()
	}
}

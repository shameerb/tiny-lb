package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

/*
todo: keep the strategy code in a seperate interface (strategy design pattern)
*/

type Key int

const (
	Attempts Key = iota
	Retry
)

type Backend struct {
	url    *url.URL
	active bool
	mu     sync.RWMutex
	proxy  *httputil.ReverseProxy
}

func (b *Backend) IsActive() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.active
}

func (b *Backend) SetActive(active bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active = active
}

type ServerPool struct {
	backends   []*Backend
	currentIdx uint64
}

func (s *ServerPool) NextIndex() int {
	return int(atomic.AddUint64(&s.currentIdx, 1)) % len(s.backends)
}

func (s *ServerPool) MarkBackendServer(url *url.URL, active bool) {
	for _, b := range s.backends {
		if b.url.String() == url.String() {
			b.SetActive(active)
			return
		}
	}
}

// todo: You can use an interface with strategies to figure out the next server by various types (roun robin, least connection, random etc)
func (s *ServerPool) GetNextServer() *Backend {
	nxt := s.NextIndex()
	// check if alive else get the next one.
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
		conn, err := net.Dial("tcp", backend.url.Host)
		if err != nil {
			backend.SetActive(false)
			return
		}
		backend.SetActive(true)
		conn.Close()
	}
}

// todo: if the backend died. Do a retry for a set number of times and if its still down, then notify and remove from the pool.
// you will need a mutex if you are editing the backend servers.
// todo: notify if the set number of servers in the pool is below some threshold. Should be backed by config per service.
func HealthCheck() {
	t := time.NewTicker(time.Second * 10)
	for range t.C {
		serverPool.HealthCheck()
	}
}

/* type Selector interface {
	GetNext() *Backend
}
*/

func lb(w http.ResponseWriter, r *http.Request) {
	// get teh next index.
	// todo: change the function to serverPool.selector.GetNextServer. selector being the strategy interface
	nxt := serverPool.GetNextServer()
	if nxt == nil {
		http.Error(w, "Service not available", http.StatusServiceUnavailable)
		return
	}
	nxt.proxy.ServeHTTP(w, r)
}

func (s *ServerPool) AddServer(server string) {
	url, err := url.Parse(server)
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(url)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		var retries int
		var ok bool
		// check if the nos of retries on error is within limit. Otherwise chuck this server out of the pool
		retries, ok = r.Context().Value(Retry).(int)
		if !ok {
			retries = 1
		}
		if retries < 3 {
			<-time.After(10 * time.Millisecond)
			ctx := context.WithValue(r.Context(), Retry, retries+1)
			proxy.ServeHTTP(w, r.WithContext(ctx))
		}

		// since the request failed even after 3 retries disable the backend.
		// this is a temp solution. In production ideally the request itself might be incorrect.
		serverPool.MarkBackendServer(url, false)

		// if requests fail. retry for 3 more times.
		// if it still failed after 3 times, change the lb downstream
		var attempts int
		attempts, ok = r.Context().Value(Attempts).(int)
		if !ok {
			attempts = 0
		}
		if attempts > 3 {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		ctx := context.WithValue(r.Context(), Attempts, attempts+1)
		lb(w, r.WithContext(ctx))
	}

	s.backends = append(s.backends, &Backend{
		url:    url,
		active: true,
		proxy:  proxy,
	})
}

var serverPool ServerPool

func main() {
	var servers string
	var port int
	flag.IntVar(&port, "port", 3030, "port")
	flag.StringVar(&servers, "backends", "", "backend servers list")
	flag.Parse()

	if len(servers) == 0 {
		log.Fatal("no servers to load balance")
	}

	// add the servers to the pool
	for _, server := range strings.Split(servers, ",") {
		serverPool.AddServer(server)
	}

	// start go routine to check health of each backend servers in a time interval
	go HealthCheck()

	s := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(lb),
	}

	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err.Error())
	}
}

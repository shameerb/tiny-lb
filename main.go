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

type Key int

const (
	Attempts Key = iota
	Retry
)

type Backend struct {
	URL          *url.URL
	Alive        bool
	mu           sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

func (b *Backend) SetAlive(alive bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Alive = alive
}

func (b *Backend) IsAlive() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Alive
}

type ServerPool struct {
	backends []*Backend
	current  uint64
}

func (s *ServerPool) AddBackend(backend *Backend) {
	s.backends = append(s.backends, backend)
}

func (s *ServerPool) NextIndex() int {
	return int(atomic.AddUint64(&s.current, uint64(1)) % uint64((len(s.backends))))
}

func (s *ServerPool) MarkBackendStatus(url *url.URL, alive bool) {
	for _, b := range s.backends {
		if b.URL.String() == url.String() {
			b.SetAlive(alive)
			return
		}
	}
}

// todo: You can use an interface with strategies to figure out the next server by various types (roun robin, least connection, random etc)
func (s *ServerPool) GetNext() *Backend {
	nxt := s.NextIndex()
	for i := nxt; i < nxt+len(s.backends); i++ {
		idx := i % len(s.backends)
		if s.backends[idx].IsAlive() {
			if i != nxt {
				atomic.StoreUint64(&s.current, uint64(idx))
			}
			return s.backends[idx]
		}
	}
	return nil
}

func (s *ServerPool) HealthCheck() {
	for _, b := range s.backends {
		alive := isBackendAlive(b.URL)
		b.SetAlive(alive)
		// todo: if the backend died. Do a retry for a set number of times and if its still down, then notify and remove from the pool.
		// todo: notify if the set number of servers in the pool is below some threshold. Should be backed by config per service.
		log.Printf("%s [%t]\n", b.URL, alive)
	}
}

func isBackendAlive(u *url.URL) bool {
	conn, err := net.DialTimeout("tcp", u.Host, 2*time.Second)
	if err != nil {
		log.Printf("Server down: %s", err)
		return false
	}
	defer conn.Close()
	return true
}

func lbHandler(w http.ResponseWriter, r *http.Request) {
	s := serverPool.GetNext()
	if s == nil {
		http.Error(w, "Service not available", http.StatusServiceUnavailable)
		return
	}
	s.ReverseProxy.ServeHTTP(w, r)
}

func healthCheck() {
	t := time.NewTicker(time.Second * 10)
	// todo: change to a range instead.
	for range t.C {
		log.Println("Health check..")
		serverPool.HealthCheck()
	}
}

func getRetries(r *http.Request) int {
	if retry, ok := r.Context().Value(Retry).(int); ok {
		return retry
	}
	return 1
}

func getAttempts(r *http.Request) int {
	if a, ok := r.Context().Value(Attempts).(int); ok {
		return a
	}
	return 0
}

var serverPool ServerPool

func main() {
	var serverList string
	var port int
	flag.StringVar(&serverList, "backends", "", "Backend servers")
	flag.IntVar(&port, "port", 3030, "Server Port")
	flag.Parse()

	if len(serverList) == 0 {
		log.Fatal("no servers to load balance")
	}

	// parse the serverList and add it to the pool
	for _, backendServer := range strings.Split(serverList, ",") {
		url, err := url.Parse(backendServer)
		if err != nil {
			log.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(url)
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
			log.Printf("%s, Error: , %s", url, e.Error())
			// check if the nos of retries on error is within limit. Otherwise chuck this server out of the pool
			if retries := getRetries(r); retries < 3 {
				<-time.After(10 * time.Millisecond)
				// retry request on the same proxy after 10 milliseconds.
				ctx := context.WithValue(r.Context(), Retry, retries+1)
				proxy.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// mark the server as inactive after 3 retries.
			serverPool.MarkBackendStatus(url, false)
			// attempts define the number of switches to another lb.
			attempts := getAttempts(r)
			if attempts > 3 {
				log.Printf("max attempts reached, terminating the request: %s", r.URL.Path)
				http.Error(w, "Service not available", http.StatusServiceUnavailable)
				return
			}
			ctx := context.WithValue(r.Context(), Attempts, attempts+1)
			lbHandler(w, r.WithContext(ctx))
		}

		serverPool.AddBackend(&Backend{
			URL:          url,
			Alive:        true,
			ReverseProxy: proxy,
		})
		log.Printf("Added server: %s\n", backendServer)
	}

	// start go routine to check health of each backend servers in a time interval
	go healthCheck()

	s := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(lbHandler),
	}
	log.Printf("Starting Load Balancer at: %d\n", port)
	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err.Error())
	}
}

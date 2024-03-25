package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/shameerb/tiny-lb/pkg/serverpool"
)

var sp serverpool.ServerPool

func HealthCheck() {
	for range time.Tick(time.Second * 10) {
		sp.HealthCheck()
	}
}

func main() {
	var servers string
	var port int
	flag.IntVar(&port, "port", 3030, "port")
	flag.StringVar(&servers, "backends", "", "backend servers to listen to")
	flag.Parse()
	fmt.Println("Port ", port)
	if len(servers) == 0 {
		log.Fatal("no servers to load balancer")
	}

	for _, s := range strings.Split(servers, ",") {
		sp.AddServer(s)
	}

	// run health checks every 10 seconds
	go HealthCheck()

	// start an http server to listen
	s := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(sp.Lb),
	}
	if err := s.ListenAndServe(); err != nil {
		log.Fatal(err.Error())
	}

}

package main

import (
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var numMetrics = flag.Int("metrics", 100, "number of metrics to return from the endpoint")
var port = flag.Int("port", 8080, "port to serve metrics on")

func main() {
	flag.Parse()
	for i := 0; i < *numMetrics; i++ {
		metric := promauto.NewCounter(prometheus.CounterOpts{
			Name: fmt.Sprintf("metric_%d", i),
			Help: fmt.Sprintf("Metric number %d", i),
		})
		go func() {
			for {
				metric.Inc()
				time.Sleep(time.Second)
			}
		}()
	}
	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(fmt.Sprintf(":%d", *port), nil)
}

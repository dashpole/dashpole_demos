package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv"
	"google.golang.org/grpc"
)

func main() {
	log.Println("Starting tracing demo...")
	opts := []otlp.ExporterOption{
		otlp.WithInsecure(),
		otlp.WithGRPCDialOption(grpc.WithBlock()), // block until connection is ready
	}
	host, found := os.LookupEnv("OTEL_EXPORTER_OTLP_HOST")
	if found {
		addr := host + ":55680"
		log.Printf("Sending spans to address: %v\n", addr)
		opts = append(opts, otlp.WithAddress(addr))
	}
	addr, found := os.LookupEnv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if found {
		log.Printf("Sending spans to address: %v\n", addr)
		opts = append(opts, otlp.WithAddress(addr))
	}
	exporter, err := otlp.NewExporter(opts...)
	if err != nil {
		log.Fatalf("Error creating otlp exporter: %v", err)
	}
	log.Println("Finished setting up exporter.")

	tracerProvider := sdktrace.NewTracerProvider(
		// Use always sample for demos
		sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sdktrace.AlwaysSample()}),
		sdktrace.WithResource(resource.New(
			// the service name used to display traces in backends
			semconv.ServiceNameKey.String("tracing-demo"),
		)),
		sdktrace.WithSyncer(exporter),
	)

	global.SetTracerProvider(tracerProvider)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handler)
	otelMux := otelhttp.NewHandler(mux, "ServeDemo")
	log.Println("Serving on port 80.")
	http.ListenAndServe(":80", otelMux)
}

func handler(w http.ResponseWriter, req *http.Request) {
	log.Println("Serving Request...")
	time.Sleep(time.Second)
	ctx := req.Context()
	tracer := global.Tracer("demo-tracer")
	ctx, innerSpan := tracer.Start(ctx, "CustomSpan")
	time.Sleep(time.Second)
	innerSpan.End()
	time.Sleep(time.Second)
}

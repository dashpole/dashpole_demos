package main

import (
	"context"
	"log"
	"os"
	"time"

	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv"
	"google.golang.org/grpc"
)

func main() {
	log.Println("Starting tracing demo...")
	exporter, err := otlp.NewExporter(
		otlp.WithInsecure(),
		otlp.WithAddress("otel-collector.opentelemetry:55680"),
		otlp.WithGRPCDialOption(grpc.WithBlock()), // block until connection is ready
	)
	if err != nil {
		log.Fatalf("Error creating otlp exporter: %v", err)
	}

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

	tracer := global.Tracer("demo-tracer")

	ctx := context.Background()
	ctx, outerSpan := tracer.Start(ctx, "OuterSpan")
	time.Sleep(time.Second)

	ctx, innerSpan := tracer.Start(ctx, "InnerSpan")
	time.Sleep(time.Second)
	innerSpan.End()

	time.Sleep(time.Second)
	outerSpan.End()

	log.Println("Finished tracing demo...")
	os.Exit(0)
}

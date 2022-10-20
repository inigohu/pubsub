package otel

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric/aggregator/histogram"
	controller "go.opentelemetry.io/otel/sdk/metric/controller/basic"
	processor "go.opentelemetry.io/otel/sdk/metric/processor/basic"
	"go.opentelemetry.io/otel/sdk/metric/selector/simple"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func InitProvider(ctx context.Context, name, host string) ([]func(context.Context) error, error) {
	var stoppers []func(context.Context) error

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithAttributes(
			// the service name used to display traces in backends
			semconv.ServiceNameKey.String(name),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// ***************************************
	// *************** Traces ****************
	// ***************************************

	conn, err := grpc.DialContext(ctx, host, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC connection to collector: %w", err)
	}

	// Set up a trace exporter
	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	// Register the trace exporter with a TracerProvider, using a batch
	// span processor to aggregate spans before export.
	bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	)
	otel.SetTracerProvider(tracerProvider)

	// set global propagator to tracecontext (the default is no-op).
	otel.SetTextMapPropagator(propagation.TraceContext{})

	stoppers = append(stoppers, tracerProvider.Shutdown)

	_ = otel.Tracer(name)

	// ***************************************
	// *************** Metrics ***************
	// ***************************************

	// Create a new gRPC metric client and set up a metric exporter.
	client := otlpmetricgrpc.NewClient(
		otlpmetricgrpc.WithInsecure(),
		otlpmetricgrpc.WithEndpoint(host),
	)

	metricExporter, err := otlpmetric.New(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("group: couldn't set up a metric exporter: %w", err)
	}
	stoppers = append(stoppers, metricExporter.Shutdown)

	// Fixed boundaries from 5ms to 10s
	boundaries := []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000}

	// New Controller with the configuration of metric export pipeline.
	pusher := controller.New(
		processor.NewFactory(
			simple.NewWithHistogramDistribution(histogram.WithExplicitBoundaries(boundaries)),
			metricExporter,
		),
		controller.WithExporter(metricExporter),
		controller.WithCollectPeriod(10*time.Second),
		controller.WithResource(res),
	)
	// Set global metric provider.
	global.SetMeterProvider(pusher)

	if err := pusher.Start(ctx); err != nil {
		return nil, fmt.Errorf("group: couldn't start pushing metrics: %w", err)
	}
	stoppers = append(stoppers, pusher.Stop)

	_ = global.Meter(name)

	return stoppers, nil
}

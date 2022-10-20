package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/pubsub"
	otelObs "github.com/cloudevents/sdk-go/observability/opentelemetry/v2/client"
	cepubsub "github.com/cloudevents/sdk-go/protocol/pubsub/v2"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	ceClient "github.com/cloudevents/sdk-go/v2/client"
	"github.com/inigohu/pubsub/pkg/example"
	otelpkg "github.com/inigohu/pubsub/pkg/otel"
	"github.com/peterbourgon/ff/v3"
	"go.opentelemetry.io/otel"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric/global"
	"go.opentelemetry.io/otel/metric/instrument/syncint64"
)

func main() {
	fs := flag.NewFlagSet("sender", flag.ContinueOnError)
	var (
		projectID           = fs.String("project", "", "GOOGLE_CLOUD_PROJECT (also via PROJECTID)")
		topicID             = fs.String("topic", "", "PUBSUB_TOPIC (also via TOPICID)")
		frequency           = fs.Duration("frequency", 1*time.Second, "send frequency in milliseconds (also via FREQUENCY)")
		instrumentationHost = fs.String("instrumentation-host", "", "OpenTelemetry Collector Host (also via INSTRUMENTATION_HOST)")
	)
	if err := ff.Parse(fs, os.Args[1:], ff.WithEnvVarNoPrefix()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("ProjectID %s, TopicID %s, Frequency %s, InstrumentationHost %s\n", *projectID, *topicID, *frequency, *instrumentationHost)

	ctx := context.Background()

	if *instrumentationHost != "" {
		shutdowns, err := otelpkg.InitProvider(ctx, "sender", *instrumentationHost)
		if err != nil {
			log.Fatal(err)
		}
		defer func() {
			for i := len(shutdowns) - 1; i >= 0; i-- {
				if err := shutdowns[i](ctx); err != nil {
					log.Fatal("failed to shutdown TracerProvider: %w", err)
				}
			}

		}()
	}

	pubsubClient, err := pubsub.NewClient(ctx, *projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	_, _ = pubsubClient.CreateTopic(ctx, *topicID)

	protocol, err := cepubsub.New(ctx, cepubsub.WithClient(pubsubClient), cepubsub.WithTopicID(*topicID))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	c, err := cloudevents.NewClient(protocol, cloudevents.WithUUIDs(), ceClient.WithObservabilityService(otelObs.NewOTelObservabilityService()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
	ticker := time.NewTicker(*frequency)

	var eventCounter syncint64.Counter
	if *instrumentationHost != "" {
		meter := global.Meter("sender")
		eventCounter, err = meter.SyncInt64().Counter("cloudevent.event.sent")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	for {
		select {
		case <-signalChan:
			log.Println("Got SIGINT...")
			os.Exit(0)
		case t := <-ticker.C:
			ctx, span := otel.Tracer("sender").Start(ctx, "Sending event...")

			event := cloudevents.NewEvent()
			event.SetType("com.cloudevents.sample.sent")
			event.SetSource("github.com/cloudevents/sdk-go/samples/pubsub/sender/")
			_ = event.SetData("application/json", &example.Data{
				Sequence: int(t.Unix()),
				Message:  "HELLO",
			})

			otelObs.InjectDistributedTracingExtension(ctx, event)

			if result := c.Send(ctx, event); cloudevents.IsUndelivered(result) {
				log.Printf("result: %v", result)
				log.Printf("failed to send: %v", err)
				span.SetStatus(otelcodes.Error, "failed to send")
				os.Exit(1)
			} else {
				if *instrumentationHost != "" {
					span.SetStatus(otelcodes.Ok, "event sent")
					eventCounter.Add(ctx, 1)
				}
				log.Printf("sent, accepted: %t", cloudevents.IsACK(result))
			}

			span.End()
		}
	}
}

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	otelObs "github.com/cloudevents/sdk-go/observability/opentelemetry/v2/client"
	cepubsub "github.com/cloudevents/sdk-go/protocol/pubsub/v2"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/event"
	"github.com/inigohu/pubsub/pkg/example"
	otelpkg "github.com/inigohu/pubsub/pkg/otel"
	"github.com/peterbourgon/ff/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/global"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	fs := flag.NewFlagSet("receiver", flag.ContinueOnError)
	var (
		projectID           = fs.String("project", "", "GOOGLE_CLOUD_PROJECT (also via PROJECTID)")
		topicID             = fs.String("topic", "", "PUBSUB_TOPIC (also via TOPICID)")
		subscriptionID      = fs.String("subscription", "", "PUBSUB_SUBSCRIPTION (also via SUBSCRIPTIONID)")
		instrumentationHost = fs.String("instrumentation-host", "", "OpenTelemetry Collector Host (also via INSTRUMENTATION_HOST)")
		sleep               = fs.Duration("sleep", 0, "sleep receiver function (also via SLEEP)")
		processors          = fs.Int("processors", 1, "number of processors receiving messages (also via PROCESSORS)")
	)
	if err := ff.Parse(fs, os.Args[1:], ff.WithEnvVarNoPrefix()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("ProjectID %s, TopicID %s, SubscriptionID %s, InstrumentationHost %s, Sleep %s\n", *projectID, *topicID, *subscriptionID, *instrumentationHost, *sleep)

	ctx := context.Background()

	if *instrumentationHost != "" {
		shutdowns, err := otelpkg.InitProvider(ctx, "receiver", *instrumentationHost)
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
	defer pubsubClient.Close()

	meter := global.Meter("receiver")
	eventCounter, err := meter.SyncInt64().Counter("cloudevent.event.received")
	if err != nil {
		log.Fatalf("failed to create counter, %s", err.Error())
	}

	wg := new(sync.WaitGroup)
	wg.Add(*processors)

	for i := 1; i <= *processors; i++ {
		sub := fmt.Sprintf("%s%v", *subscriptionID, i)

		ok, err := pubsubClient.Subscription(sub).Exists(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		if !ok {
			_, err := pubsubClient.CreateSubscription(ctx, sub, pubsub.SubscriptionConfig{
				Topic: pubsubClient.Topic(*topicID),
			})
			if err != nil && status.Code(err) != codes.AlreadyExists {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
		}

		t, err := cepubsub.New(ctx,
			cepubsub.WithClient(pubsubClient),
			cepubsub.WithSubscriptionAndTopicID(sub, *topicID),
		)
		if err != nil {
			log.Fatalf("failed to create pubsub protocol, %s", err.Error())
		}
		c, err := cloudevents.NewClient(t)

		if err != nil {
			log.Fatalf("failed to create client, %s", err.Error())
		}

		log.Println("Created client, listening...")

		go func() {
			defer t.Close(ctx)
			defer wg.Done()
			if err := c.StartReceiver(ctx, func(ctx context.Context, event event.Event) error {
				ctx = otelObs.ExtractDistributedTracingExtension(ctx, event)
				_, span := otel.Tracer("receiver").Start(ctx, "receive")
				eventCounter.Add(ctx, 1, attribute.String("subscription", sub))
				span.SetAttributes(attribute.String("subscription", sub))
				defer span.End()

				time.Sleep(*sleep)

				data := &example.Data{}
				if err := event.DataAs(data); err != nil {
					fmt.Printf("Got Data Error: %s\n", err.Error())
				}
				fmt.Printf("Data: %+v\n", data)
				return nil
			}); err != nil {
				log.Fatalf("failed to start pubsub receiver, %s", err.Error())
			}
		}()

	}
	wg.Wait()
}

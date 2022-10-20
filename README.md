# pubsub
PubSub testing project

This project aims to test the GCP PubSub service using the [CloudEvents](https://github.com/cloudevents/sdk-go) library. In addition, the sender/receiver services are instrumented using [OpenTelemetry](https://github.com/open-telemetry/opentelemetry-go).

Run sender: `go run cmd/sender/main.go`

Run receiver: `go run cmd/receiver/main.go`

Run docker-compose: `docker compose up`
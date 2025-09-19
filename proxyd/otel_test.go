package proxyd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func TestMetricsClient(t *testing.T) {
	client, err := NewMetricsClient(context.Background(), "fullnode-test-service", "")
	assert.NoError(t, err)
	defer client.Close(context.Background())
	t.Run("Counter", func(t *testing.T) {
		counter, err := client.Counter("fullnode_test_counter", "Counter")
		assert.NoError(t, err)
		counter.Add(context.Background(), 1,
			metric.WithAttributes(attribute.String("from", "nodeone")))
	})
	t.Run("Gauge", func(t *testing.T) {
		err := client.GaugeRecord(context.Background(), "fullnode_test_gauge", "Gauge",
			100.0,
			attribute.String("from", "nodeone"))
		assert.NoError(t, err)
	})
	t.Run("Histogram", func(t *testing.T) {
		histogram, err := client.Histogram("fullnode_test_histogram", "Histogram")
		assert.NoError(t, err)
		histogram.Record(context.Background(), 0.5,
			metric.WithAttributes(attribute.String("from", "nodeone")))
	})
	time.Sleep(3 * time.Second)
}

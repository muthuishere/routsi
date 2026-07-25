package backend

import (
	"context"

	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/queue"
)

// QueueBackend answers by handing the request to the broker, which blocks until
// a registered pull-worker returns an answer (ADR-001). Non-streaming in v1:
// a worker returns a whole answer, so Stream fakes off Complete like the other
// agent backends.
type QueueBackend struct {
	broker *queue.Broker
	name   string
}

func NewQueue(broker *queue.Broker, name string) *QueueBackend {
	return &QueueBackend{broker: broker, name: name}
}

func (q *QueueBackend) Complete(ctx context.Context, req *api.ChatRequest) (string, error) {
	return q.broker.Submit(ctx, q.name, req)
}

func (q *QueueBackend) Stream(ctx context.Context, req *api.ChatRequest, emit func(string)) error {
	text, err := q.Complete(ctx, req)
	if err != nil {
		return err
	}
	emit(text)
	return nil
}

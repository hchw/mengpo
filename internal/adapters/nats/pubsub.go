package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/ports"
	natsgo "github.com/nats-io/nats.go"
)

// Adapter sends only wake-up notifications. Jobs and recovery state remain in PostgreSQL.
type Adapter struct {
	connection *natsgo.Conn
	subject    string
}

func New(connection *natsgo.Conn, subject string) (*Adapter, error) {
	if connection == nil || subject == "" {
		return nil, errors.New("NATS connection and subject are required")
	}
	return &Adapter{connection: connection, subject: subject}, nil
}

func (a *Adapter) Publish(ctx context.Context, notification ports.JobNotification) error {
	if notification.TenantID == "" || notification.JobID == "" {
		return errors.New("tenant and job identifiers are required")
	}
	payload, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := a.connection.Publish(a.subject, payload); err != nil {
		return fmt.Errorf("publish job notification: %w", err)
	}
	return nil
}

func (a *Adapter) Subscribe(ctx context.Context) (<-chan ports.JobNotification, error) {
	out := make(chan ports.JobNotification, 64)
	in := make(chan *natsgo.Msg, 64)
	subscription, err := a.connection.ChanSubscribe(a.subject, in)
	if err != nil {
		close(out)
		return nil, fmt.Errorf("subscribe to job notifications: %w", err)
	}
	go func() {
		defer close(out)
		defer subscription.Unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case message, ok := <-in:
				if !ok {
					return
				}
				var notification ports.JobNotification
				if json.Unmarshal(message.Data, &notification) != nil || notification.TenantID == "" || notification.JobID == "" {
					continue
				}
				select {
				case out <- notification:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

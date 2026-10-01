package nats

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
	natsgo "github.com/nats-io/nats.go"
)

func TestAdapterPublishSubscribe(t *testing.T) {
	url := os.Getenv("NATS_TEST_URL")
	if url == "" {
		t.Skip("set NATS_TEST_URL to run NATS integration test")
	}
	connection, err := natsgo.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	adapter, err := New(connection, "test.memory.jobs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifications, err := adapter.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	want := ports.JobNotification{TenantID: "tenant", JobID: "job"}
	if err := adapter.Publish(ctx, want); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-notifications:
		if got != want {
			t.Fatalf("notification=%#v want %#v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NATS notification")
	}
}

func TestPublishRequiresTenantIdentity(t *testing.T) {
	url := os.Getenv("NATS_TEST_URL")
	if url == "" {
		t.Skip("set NATS_TEST_URL to run NATS integration test")
	}
	connection, err := natsgo.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	adapter, err := New(connection, "test.memory.jobs")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Publish(context.Background(), ports.JobNotification{JobID: "job"}); err == nil {
		t.Fatal("publish without tenant identity was accepted")
	}
}

func TestNotificationsKeepTenantsSeparateAndDropUnboundPayloads(t *testing.T) {
	url := os.Getenv("NATS_TEST_URL")
	if url == "" {
		t.Skip("set NATS_TEST_URL to run NATS integration test")
	}
	connection, err := natsgo.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	adapter, err := New(connection, "test.memory.jobs.tenant")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifications, err := adapter.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	// An unbound payload (no tenant) must be dropped by the subscriber.
	if err := connection.Publish("test.memory.jobs.tenant", []byte(`{"job_id":"j0"}`)); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Publish(ctx, ports.JobNotification{TenantID: "tenant-a", JobID: "job-a"}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Publish(ctx, ports.JobNotification{TenantID: "tenant-b", JobID: "job-b"}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for len(seen) < 2 {
		select {
		case got := <-notifications:
			seen[got.TenantID] = got.JobID
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out; seen=%#v", seen)
		}
	}
	if seen["tenant-a"] != "job-a" || seen["tenant-b"] != "job-b" {
		t.Fatalf("tenant attribution lost: %#v", seen)
	}
}

package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/hchw/mengpo/internal/assembly"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	assembly.RunMainServer(ctx)
}

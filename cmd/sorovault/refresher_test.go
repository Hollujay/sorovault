package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
)

type blockingNetworkFetcher struct {
	stellar.Fetcher
	started   chan struct{}
	cancelled chan struct{}
	finish    chan struct{}
}

func (f *blockingNetworkFetcher) Network(ctx context.Context) (string, error) {
	close(f.started)
	<-ctx.Done()
	close(f.cancelled)
	<-f.finish
	return "", ctx.Err()
}

func TestStartRefresherNoOpWhenDisabled(t *testing.T) {
	t.Parallel()

	refresh, stop, err := startRefresher(context.Background(), &app{cfg: &config.Config{}})
	assert.NoError(t, err)
	assert.Nil(t, refresh)
	stop()
}

func TestStartRefresherStartsCancelsAndDrains(t *testing.T) {
	t.Parallel()

	fetcher := &blockingNetworkFetcher{
		Fetcher:   stellartest.New(),
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
		finish:    make(chan struct{}),
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &app{
		cfg: &config.Config{RefreshInterval: time.Minute, RPCTimeout: time.Second},
		log: log,
		registry: registry.New(fetcher, spec.XDRDecoder{}, store.NewMemory(),
			registry.Options{Logger: log}),
	}
	refresh, stop, err := startRefresher(context.Background(), a)
	require.NoError(t, err)
	require.NotNil(t, refresh)
	<-fetcher.started

	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()

	select {
	case <-fetcher.cancelled:
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel the refresher")
	}
	select {
	case <-stopped:
		t.Fatal("stop returned before the in-flight refresher finished")
	default:
	}

	close(fetcher.finish)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not return after the refresher drained")
	}
	assert.NotPanics(t, stop, "calling stop again is harmless")
}

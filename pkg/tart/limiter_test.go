package tart

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLimiterBlocksThirdAcquire(t *testing.T) {
	l := newLimiter(2)
	ctx := context.Background()
	a, err := l.Acquire(ctx)
	require.NoError(t, err)
	b, err := l.Acquire(ctx)
	require.NoError(t, err)

	got := make(chan *slot)
	go func() {
		s, err := l.Acquire(ctx)
		assert.NoError(t, err)
		got <- s
	}()
	select {
	case <-got:
		t.Fatal("third acquire did not block")
	case <-time.After(100 * time.Millisecond):
	}

	a.Release()
	a.Release() // idempotent: must not free a second slot
	var c *slot
	select {
	case c = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("third acquire did not proceed after release")
	}

	tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = l.Acquire(tctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	b.Release()
	c.Release()
	assert.Len(t, l.slots, 0)
}

func TestSharedLimiter(t *testing.T) {
	assert.Same(t, sharedLimiter(2), sharedLimiter(0))
	assert.NotSame(t, sharedLimiter(2), sharedLimiter(3))
}

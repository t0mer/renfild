package api

import (
	"context"
	"testing"
	"time"
)

// contextWithTimeout bounds a streaming test so a bug cannot hang the suite.
func contextWithTimeout(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// waitFor polls a condition, failing the test if it never becomes true.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was never satisfied")
}

package testutil

import (
	"context"
	"testing"
	"time"
)

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func mustUnits(t *testing.T, sys interface {
	ListUnits(context.Context) ([]string, error)
},
) []string {
	t.Helper()
	ctx, cancel := contextWithTimeout()
	defer cancel()
	units, err := sys.ListUnits(ctx)
	if err != nil {
		return nil
	}
	return units
}

package fleet

import (
	"context"
	"errors"

	"clawbench-fleet/internal/sysops"
)

// failingCreateUser fails useradd while keeping every other operation real.
type failingCreateUser struct {
	sysops.System
}

func (failingCreateUser) CreateUser(context.Context, string, int) error {
	return errors.New("useradd: simulated failure")
}

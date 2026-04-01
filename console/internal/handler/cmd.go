package handler

import (
	"context"
	"os/exec"
)

// createCommand creates an exec.Cmd with context.
func createCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

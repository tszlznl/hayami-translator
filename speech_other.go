//go:build !windows

package main

import (
	"context"
	"os/exec"
)

func speakPlatformSystem(ctx context.Context, text string) error {
	cmd, err := speechCommand(ctx, text)
	if err != nil {
		return err
	}
	return cmd.Run()
}

func hideCommandWindow(*exec.Cmd) {}

//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func captureScreenshot(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp("", "hayami-screenshot-*.png")
	if err != nil {
		return nil, fmt.Errorf("创建截图临时文件失败: %w", err)
	}
	defer os.Remove(file.Name())
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("关闭截图临时文件失败: %w", err)
	}

	// A separate instance must finish saving before we read or remove its file.
	cmd := exec.CommandContext(ctx, "spectacle", "--region", "--background", "--nonotify", "--new-instance", "--output", file.Name())
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Spectacle 区域截图失败: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	image, err := os.ReadFile(file.Name())
	if os.IsNotExist(err) {
		return nil, context.Canceled
	}
	if err != nil {
		return nil, fmt.Errorf("读取截图失败: %w", err)
	}
	// Escape closes Spectacle successfully without writing an image.
	if len(image) == 0 {
		return nil, context.Canceled
	}
	return image, nil
}

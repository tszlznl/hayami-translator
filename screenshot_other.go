//go:build !windows && !linux

package main

import (
	"context"
	"fmt"
)

func captureScreenshot(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("当前平台暂不支持区域截图")
}

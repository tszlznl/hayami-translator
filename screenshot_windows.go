//go:build windows

package main

import (
	"context"
	"time"
)

func captureScreenshot(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before := clipboardSeq()
	startSnipping()
	image := waitClipboardImage(ctx, before, 60*time.Second)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(image) == 0 {
		return nil, context.Canceled
	}
	return image, nil
}

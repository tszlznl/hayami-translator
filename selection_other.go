//go:build !windows

package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo"
)

func clipboardSeq() uint32 {
	return 0
}

func readSelectedText() (string, error) {
	text := mygo.Clipboard.ReadText()
	if text == "" {
		return "", fmt.Errorf("当前平台暂未支持自动捕获选中文字")
	}
	return text, nil
}

func waitClipboardImage(before uint32, timeout time.Duration) []byte {
	return nil
}

func startSnipping() {}

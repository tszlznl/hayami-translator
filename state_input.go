package main

import (
	"context"

	"github.com/egoist/mygo/ui"

	"hayami/internal/dict"
)

type inputState struct {
	inputSource            string
	inputResult            string
	inputSourceLang        string
	inputTargetLang        string
	inputStreaming         bool
	inputErr               string
	inputStatus            string
	inputReqID             int
	inputCancel            context.CancelFunc
	inputWindowNeedFocus   bool
	inputWindowInputHandle ui.Handle
	inputDictEntry         *dict.Entry
	inputCacheHit          bool
}

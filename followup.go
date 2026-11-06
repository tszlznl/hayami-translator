package main

import (
	"context"
	"math"
	"strings"
	"time"
)

// cancelFollowUp stops an in-flight follow-up and invalidates its callbacks.
func (a *app) cancelFollowUp() {
	a.followUpReqID++
	if a.followUpCancel != nil {
		a.followUpCancel()
		a.followUpCancel = nil
	}
	a.followUpStreaming = false
}

func (a *app) canFollowUp() bool {
	return !a.hasImage &&
		!a.multiEngine &&
		!a.streaming &&
		strings.TrimSpace(a.result) != "" &&
		!a.followUpStreaming
}

func (a *app) showFollowUp() bool {
	return a.canFollowUp() || a.followUpStreaming || strings.TrimSpace(a.followUp) != ""
}

// submitPopup routes Ctrl+Enter: a non-empty follow-up question continues the
// current result, everything else starts a new translation.
func (a *app) submitPopup() {
	if strings.TrimSpace(a.followUpInput) != "" && !a.streaming && !a.multiEngine && strings.TrimSpace(a.result) != "" {
		a.sendFollowUp()
		return
	}
	a.startTranslation()
}

func (a *app) sendFollowUp() {
	question := strings.TrimSpace(a.followUpInput)
	if question == "" || a.hasImage || a.multiEngine || a.streaming || strings.TrimSpace(a.result) == "" {
		return
	}

	a.cancelFollowUp()
	cfg := a.cfg
	source := a.source
	result := a.result
	messages := followUpMessages(cfg, a.currentMode(), source, result, question)

	providers := cfg.EnabledProviders(false)
	configured := providers[:0]
	for _, p := range providers {
		if providerConfigured(p, p.Key) {
			configured = append(configured, p)
		}
	}
	providers = configured
	if len(providers) == 0 {
		a.followUpErr = "没有可用的翻译服务"
		return
	}

	a.followUpInput = ""
	a.followUpQ = question
	a.followUp = ""
	a.followUpErr = ""
	a.followUpStreaming = true
	a.popupScroll.Y = math.MaxFloat32

	ctx, cancel := context.WithCancel(context.Background())
	a.followUpCancel = cancel
	a.followUpReqID++
	id := a.followUpReqID

	go func() {
		var pending strings.Builder
		flush := func() {
			if pending.Len() == 0 {
				return
			}
			chunk := pending.String()
			pending.Reset()
			a.popup.Update(func() {
				if a.followUpReqID == id {
					a.followUp += chunk
				}
			})
		}
		last := time.Now()

		err := translateWithFallback(ctx, cfg, providers, messages, source, nil, func(delta string) {
			pending.WriteString(delta)
			if time.Since(last) >= 30*time.Millisecond {
				flush()
				last = time.Now()
			}
		})
		flush()

		a.popup.Update(func() {
			if a.followUpReqID != id {
				return
			}
			a.followUpStreaming = false
			if err != nil && ctx.Err() == nil {
				title, hint := explainError(err)
				a.followUpErr = title
				if hint != "" {
					a.followUpErr += "。" + hint
				}
			}
		})
	}()
}

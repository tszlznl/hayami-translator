package main

import (
	"context"
	"log"
	"math"
	"strings"
	"time"
)

func toHistoryFollowUpTurns(turns []promptFollowUpTurn) []historyFollowUpTurn {
	if len(turns) == 0 {
		return nil
	}
	out := make([]historyFollowUpTurn, len(turns))
	for i, t := range turns {
		out[i] = historyFollowUpTurn{
			Question: t.Question,
			Answer:   t.Answer,
		}
	}
	return out
}

func fromHistoryFollowUpTurns(turns []historyFollowUpTurn) []promptFollowUpTurn {
	if len(turns) == 0 {
		return nil
	}
	out := make([]promptFollowUpTurn, len(turns))
	for i, t := range turns {
		out[i] = promptFollowUpTurn{
			Question: t.Question,
			Answer:   t.Answer,
		}
	}
	return out
}

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
	return a.canFollowUp() || a.followUpStreaming || len(a.followUpTurns) > 0 || strings.TrimSpace(a.followUp) != ""
}

// submitPopup routes Ctrl+Enter: a non-empty follow-up question continues the
// current result, everything else starts a new translation.
func (a *app) submitPopup() {
	if a.streaming {
		return
	}
	if strings.TrimSpace(a.followUpInput) != "" && a.canFollowUp() {
		a.sendFollowUp()
		return
	}
	if !a.followUpStreaming {
		a.startTranslation()
	}
}

func (a *app) sendFollowUp() {
	question := strings.TrimSpace(a.followUpInput)
	if question == "" || !a.canFollowUp() {
		return
	}

	a.ensureProviderKeysLoaded()
	a.cancelFollowUp()
	cfg := a.cfg
	source := a.source
	result := a.result
	cfg.TargetLang = effectiveTarget(a.effectivePopupTarget(), source)
	messages := followUpMessages(cfg, a.currentMode(), source, result, a.followUpTurns, question)

	providers := cfg.EnabledPromptProviders()
	configured := providers[:0]
	for _, p := range providers {
		if providerConfigured(p, p.Key) {
			configured = append(configured, p)
		}
	}
	providers = configured
	if len(providers) == 0 {
		a.followUpErr = "追问需要启用一个配置好的 OpenAI Compatible 服务"
		return
	}

	a.followUpInput = ""
	a.followUpQ = question
	a.followUp = ""
	a.followUpErr = ""
	a.followUpStreaming = true
	a.popupAutoScroll = true
	a.popupScroll.Y = math.MaxFloat32

	ctx, cancel := context.WithCancel(context.Background())
	a.followUpCancel = cancel
	a.followUpReqID++
	id := a.followUpReqID

	go func() {
		defer cancel()
		var pending strings.Builder
		flushedFirst := false
		flush := func() {
			if pending.Len() == 0 {
				return
			}
			chunk := pending.String()
			pending.Reset()
			flushedFirst = true
			a.postUI(func() {
				if a.followUpReqID == id {
					a.followUp += chunk
				}
			})
		}
		last := time.Now()

		err := translateWithFallback(ctx, cfg, providers, messages, source, nil, func(delta string) {
			pending.WriteString(delta)
			if !flushedFirst || time.Since(last) >= 30*time.Millisecond {
				flush()
				last = time.Now()
			}
		})
		flush()
		wasCanceled := ctx.Err() != nil

		a.postUI(func() {
			if a.followUpReqID != id {
				return
			}
			a.followUpStreaming = false
			if err != nil && !wasCanceled {
				title, hint := explainError(err)
				a.followUpErr = title
				if hint != "" {
					a.followUpErr += "。" + hint
				}
				return
			}
			if !wasCanceled && strings.TrimSpace(a.followUp) != "" {
				a.followUpTurns = append(a.followUpTurns, promptFollowUpTurn{
					Question: a.followUpQ,
					Answer:   a.followUp,
				})
				a.followUpQ = ""
				a.followUp = ""
				a.followUpErr = ""

				if a.store != nil && a.activeHistoryID > 0 {
					histID := a.activeHistoryID
					turns := toHistoryFollowUpTurns(a.followUpTurns)
					go func() {
						if err := a.store.UpdateFollowUps(histID, turns); err != nil {
							log.Print("history follow-up update: ", err)
							return
						}
						a.refreshHistory()
					}()
				}
			}
		})
	}()
}

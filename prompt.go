package main

import (
	"hayami/internal/config"
	"hayami/internal/prompt"
)

type promptParams = prompt.Params
type promptFollowUpTurn = prompt.FollowUpTurn

const (
	materialContract    = prompt.MaterialContract
	instructionContract = prompt.InstructionContract
	followUpContract    = prompt.FollowUpContract
	imageContract       = prompt.ImageContract
)

func systemInstruction(c Config, p, task, contract string) string {
	return prompt.SystemInstruction(c, p, task, contract)
}

func outputLanguage(cfg Config, m config.Mode, source string) string {
	return prompt.OutputLanguage(cfg, m, source)
}

func messagesForMode(cfg Config, source string, m config.Mode) []chatMessage {
	return prompt.MessagesForMode(cfg, source, m)
}

func textMessages(cfg Config, source string) []chatMessage { return prompt.TextMessages(cfg, source) }

func followUpMessages(cfg Config, m config.Mode, source, result string, turns []promptFollowUpTurn, question string) []chatMessage {
	return prompt.FollowUpMessages(cfg, m, source, result, turns, question)
}

func imageMessages(cfg Config, png []byte) []chatMessage {
	return prompt.ImageMessages(cfg, png, "image/png")
}

func imageMessagesWithMIME(cfg Config, data []byte, mime string) []chatMessage {
	return prompt.ImageMessages(cfg, data, mime)
}

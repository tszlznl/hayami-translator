package main

import "hayami/internal/history"

type historyEntry = history.Entry
type historyStore = history.Store

func openHistory() (*historyStore, error) { return history.Open() }

func openHistoryAt(path string) (*historyStore, error) { return history.OpenAt(path) }

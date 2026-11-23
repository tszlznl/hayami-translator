package main

import (
	"github.com/egoist/mygo"
	"hayami/internal/secrets"
)

const currentAppIdentifier = secrets.AppIdentifier

func appSecrets() mygo.SecretsModule { return secrets.AppSecrets() }

func loadProviderKeys(cfg Config) Config { _, _ = secrets.LoadProviderKeys(cfg.Providers); return cfg }

func storedSecret(key string) ([]byte, error) { return secrets.StoredSecret(key) }

func saveProviderKeys(cfg Config) error { return secrets.SaveProviderKeys(cfg.Providers) }

func deleteProviderKey(p Provider) { _ = secrets.DeleteProviderKey(p.ID) }

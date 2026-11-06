package main

import (
	"errors"
	"strings"

	"github.com/egoist/mygo"
)

const (
	currentAppIdentifier = "com.hayami.translator"
	apiKeySecret         = "api-key"
)

func appSecrets() mygo.SecretsModule {
	return mygo.Secrets.Service(currentAppIdentifier)
}

// loadProviderKeys fills each provider's runtime Key from the credential
// store. The old single-provider app stored its key as "api-key"; the
// migrated OpenAI provider inherits that value.
func loadProviderKeys(cfg Config) Config {
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if v, err := storedSecret(providerSecretKey(*p)); err == nil {
			p.Key = string(v)
		}
	}
	legacy, err := storedSecret(apiKeySecret)
	if err != nil || len(legacy) == 0 {
		return cfg
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.ID != "openai" || p.Kind != providerOpenAI {
			continue
		}
		if strings.TrimSpace(p.Key) == "" {
			p.Key = string(legacy)
			if err := appSecrets().Set(providerSecretKey(*p), legacy); err != nil {
				return cfg
			}
		}
		// The old key is no longer needed once the per-provider key is
		// present. Removing it also means an intentionally cleared key is
		// not silently restored on the next start.
		_ = appSecrets().Delete(apiKeySecret)
		_ = mygo.Secrets.Delete(apiKeySecret)
		break
	}
	return cfg
}

// storedSecret reads a secret from the renamed app's service and copies it
// from the old service on first use, so existing API keys survive the rename.
func storedSecret(key string) ([]byte, error) {
	store := appSecrets()
	v, err := store.Get(key)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, err
	}
	if defVal, defErr := mygo.Secrets.Get(key); defErr == nil {
		_ = store.Set(key, defVal)
		return defVal, nil
	}
	legacy := mygo.Secrets.Service(legacyAppIdentifier)
	v, err = legacy.Get(key)
	if err != nil {
		return nil, err
	}
	if err := store.Set(key, v); err == nil {
		_ = legacy.Delete(key)
	}
	return v, nil
}

func saveProviderKeys(cfg Config) error {
	store := appSecrets()
	for _, p := range cfg.Providers {
		key := strings.TrimSpace(p.Key)
		if key == "" {
			if err := store.Delete(providerSecretKey(p)); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
				return err
			}
			_ = mygo.Secrets.Delete(providerSecretKey(p))
			continue
		}
		if err := store.Set(providerSecretKey(p), []byte(key)); err != nil {
			return err
		}
	}
	return nil
}

func deleteProviderKey(p Provider) {
	_ = appSecrets().Delete(providerSecretKey(p))
	_ = mygo.Secrets.Delete(providerSecretKey(p))
}

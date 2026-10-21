package main

import (
	"errors"
	"strings"

	"github.com/egoist/mygo"
)

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
			if err := mygo.Secrets.Set(providerSecretKey(*p), legacy); err != nil {
				return cfg
			}
		}
		// The old key is no longer needed once the per-provider key is
		// present. Removing it also means an intentionally cleared key is
		// not silently restored on the next start.
		_ = mygo.Secrets.Delete(apiKeySecret)
		break
	}
	return cfg
}

// storedSecret reads a secret from the renamed app's service and copies it
// from the old service on first use, so existing API keys survive the rename.
func storedSecret(key string) ([]byte, error) {
	v, err := mygo.Secrets.Get(key)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, err
	}
	legacy := mygo.Secrets.Service(legacyAppIdentifier)
	v, err = legacy.Get(key)
	if err != nil {
		return nil, err
	}
	if err := mygo.Secrets.Set(key, v); err == nil {
		_ = legacy.Delete(key)
	}
	return v, nil
}

func saveProviderKeys(cfg Config) error {
	for _, p := range cfg.Providers {
		key := strings.TrimSpace(p.Key)
		if key == "" {
			if err := mygo.Secrets.Delete(providerSecretKey(p)); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
				return err
			}
			continue
		}
		if err := mygo.Secrets.Set(providerSecretKey(p), []byte(key)); err != nil {
			return err
		}
	}
	return nil
}

func deleteProviderKey(p Provider) {
	_ = mygo.Secrets.Delete(providerSecretKey(p))
}

// Package secrets manages provider API keys in the platform credential store
// without depending on the UI layer.
package secrets

import (
	"errors"
	"strings"

	"github.com/egoist/mygo"
	"hayami/internal/config"
)

const (
	AppIdentifier       = "com.hayami.translator"
	LegacyAppIdentifier = "com.nextai.translator"
	LegacyAPIKeySecret  = "api-key"
)

// Backend is satisfied by mygo.SecretsModule and in-memory test doubles.
type Backend interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// Store encapsulates the scoped application keyring along with default and
// legacy migration fallbacks.
type Store struct {
	Current Backend
	Default Backend
	Legacy  Backend
}

// AppSecrets returns the mygo SecretsModule scoped to AppIdentifier.
func AppSecrets() mygo.SecretsModule {
	return mygo.Secrets.Service(AppIdentifier)
}

// DefaultStore returns a Store backed by the live OS credential store.
func DefaultStore() *Store {
	return &Store{
		Current: AppSecrets(),
		Default: mygo.Secrets,
		Legacy:  mygo.Secrets.Service(LegacyAppIdentifier),
	}
}

// LoadProviderKeys fills each provider's runtime Key from the OS credential
// store, migrating legacy keys when found.
func LoadProviderKeys(providers []config.Provider) (migrated bool, err error) {
	return DefaultStore().LoadProviderKeys(providers)
}

// SaveProviderKeys persists non-empty provider keys to the OS credential store
// and deletes empty ones.
func SaveProviderKeys(providers []config.Provider) error {
	return DefaultStore().SaveProviderKeys(providers)
}

// DeleteProviderKey removes a provider's secret from the OS credential store.
func DeleteProviderKey(id string) error {
	return DefaultStore().DeleteProviderKey(id)
}

// StoredSecret reads a secret by key from the OS credential store, migrating
// from default or legacy scopes when found.
func StoredSecret(key string) ([]byte, error) {
	return DefaultStore().StoredSecret(key)
}

func (s *Store) LoadProviderKeys(providers []config.Provider) (migrated bool, err error) {
	if s == nil || s.Current == nil {
		return false, errors.New("secrets: current backend is nil")
	}
	for i := range providers {
		p := &providers[i]
		v, m, getErr := s.storedSecret(config.SecretKey(*p))
		if getErr == nil {
			p.Key = string(v)
			if m {
				migrated = true
			}
			continue
		}
		if !errors.Is(getErr, mygo.ErrSecretNotFound) {
			return migrated, getErr
		}
	}

	var openaiProvider *config.Provider
	for i := range providers {
		if providers[i].ID == "openai" && providers[i].Kind == config.ProviderOpenAI {
			openaiProvider = &providers[i]
			break
		}
	}
	if openaiProvider == nil {
		return migrated, nil
	}
	if strings.TrimSpace(openaiProvider.Key) != "" {
		s.deleteLegacyAPIKey()
		return migrated, nil
	}

	legacy, legacyErr := s.peekSecret(LegacyAPIKeySecret)
	if legacyErr != nil {
		if errors.Is(legacyErr, mygo.ErrSecretNotFound) {
			return migrated, nil
		}
		return migrated, legacyErr
	}
	trimmed := strings.TrimSpace(string(legacy))
	if trimmed == "" {
		s.deleteLegacyAPIKey()
		return migrated, nil
	}
	if err := s.Current.Set(config.SecretKey(*openaiProvider), []byte(trimmed)); err != nil {
		return migrated, err
	}
	openaiProvider.Key = trimmed
	migrated = true
	// The old key is no longer needed once the per-provider key is
	// present. Removing it also means an intentionally cleared key is
	// not silently restored on the next start.
	s.deleteLegacyAPIKey()
	return migrated, nil
}

func (s *Store) peekSecret(key string) ([]byte, error) {
	for _, b := range []Backend{s.Current, s.Default, s.Legacy} {
		if b == nil {
			continue
		}
		v, err := b.Get(key)
		if err == nil {
			return v, nil
		}
		if !errors.Is(err, mygo.ErrSecretNotFound) {
			return nil, err
		}
	}
	return nil, mygo.ErrSecretNotFound
}

func (s *Store) deleteLegacyAPIKey() {
	for _, b := range []Backend{s.Current, s.Default, s.Legacy} {
		if b == nil {
			continue
		}
		_ = b.Delete(LegacyAPIKeySecret)
	}
}

func (s *Store) deleteSecretAcrossBackends(key string) error {
	var firstErr error
	for _, b := range []Backend{s.Current, s.Default, s.Legacy} {
		if b == nil {
			continue
		}
		if err := b.Delete(key); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Store) StoredSecret(key string) ([]byte, error) {
	v, _, err := s.storedSecret(key)
	return v, err
}

func (s *Store) storedSecret(key string) ([]byte, bool, error) {
	if s == nil || s.Current == nil {
		return nil, false, errors.New("secrets: current backend is nil")
	}
	v, err := s.Current.Get(key)
	if err == nil {
		return v, false, nil
	}
	if !errors.Is(err, mygo.ErrSecretNotFound) {
		return nil, false, err
	}
	if s.Default != nil {
		if defVal, defErr := s.Default.Get(key); defErr == nil {
			if setErr := s.Current.Set(key, defVal); setErr == nil {
				return defVal, true, nil
			}
			return defVal, false, nil
		} else if !errors.Is(defErr, mygo.ErrSecretNotFound) {
			return nil, false, defErr
		}
	}
	if s.Legacy != nil {
		v, err = s.Legacy.Get(key)
		if err != nil {
			return nil, false, err
		}
		if setErr := s.Current.Set(key, v); setErr == nil {
			_ = s.Legacy.Delete(key)
			return v, true, nil
		}
		return v, false, nil
	}
	return nil, false, mygo.ErrSecretNotFound
}

func (s *Store) SaveProviderKeys(providers []config.Provider) error {
	if s == nil || s.Current == nil {
		return errors.New("secrets: current backend is nil")
	}
	for _, p := range providers {
		secretKey := config.SecretKey(p)
		key := strings.TrimSpace(p.Key)
		if key == "" {
			if err := s.deleteSecretAcrossBackends(secretKey); err != nil {
				return err
			}
			continue
		}
		if err := s.Current.Set(secretKey, []byte(key)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteProviderKey(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("secrets: provider id is required")
	}
	if s == nil || s.Current == nil {
		return errors.New("secrets: current backend is nil")
	}
	secretKey := config.SecretKey(config.Provider{ID: id})
	return s.deleteSecretAcrossBackends(secretKey)
}

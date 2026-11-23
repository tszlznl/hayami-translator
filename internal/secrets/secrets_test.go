package secrets

import (
	"errors"
	"testing"

	"github.com/egoist/mygo"
	"hayami/internal/config"
)

type memBackend struct {
	data map[string][]byte
}

func newMemBackend() *memBackend {
	return &memBackend{data: map[string][]byte{}}
}

func (m *memBackend) Get(key string) ([]byte, error) {
	v, ok := m.data[key]
	if !ok {
		return nil, mygo.ErrSecretNotFound
	}
	return append([]byte(nil), v...), nil
}

func (m *memBackend) Set(key string, value []byte) error {
	m.data[key] = append([]byte(nil), value...)
	return nil
}

func (m *memBackend) Delete(key string) error {
	if _, ok := m.data[key]; !ok {
		return mygo.ErrSecretNotFound
	}
	delete(m.data, key)
	return nil
}

type errBackend struct {
	err error
}

func (e errBackend) Get(string) ([]byte, error) { return nil, e.err }
func (e errBackend) Set(string, []byte) error   { return e.err }
func (e errBackend) Delete(string) error        { return e.err }

type spyBackend struct {
	mem         *memBackend
	getCalls    []string
	setCalls    []string
	deleteCalls []string
	getErr      func(key string) error
	setErr      func(key string) error
	deleteErr   func(key string) error
}

func newSpyBackend() *spyBackend {
	return &spyBackend{mem: newMemBackend()}
}

func (s *spyBackend) Get(key string) ([]byte, error) {
	s.getCalls = append(s.getCalls, key)
	if s.getErr != nil {
		if err := s.getErr(key); err != nil {
			return nil, err
		}
	}
	return s.mem.Get(key)
}

func (s *spyBackend) Set(key string, value []byte) error {
	s.setCalls = append(s.setCalls, key)
	if s.setErr != nil {
		if err := s.setErr(key); err != nil {
			return err
		}
	}
	return s.mem.Set(key, value)
}

func (s *spyBackend) Delete(key string) error {
	s.deleteCalls = append(s.deleteCalls, key)
	if s.deleteErr != nil {
		if err := s.deleteErr(key); err != nil {
			return err
		}
	}
	return s.mem.Delete(key)
}

func TestSaveAndLoadProviderKeysRoundTrip(t *testing.T) {
	cur := newMemBackend()
	def := newMemBackend()
	leg := newMemBackend()
	store := &Store{Current: cur, Default: def, Legacy: leg}

	toSave := []config.Provider{
		{ID: "openai", Kind: config.ProviderOpenAI, Key: "  sk-openai  "},
		{ID: "azure", Kind: config.ProviderAzure, Key: "az-key\n"},
		{ID: "baidu", Kind: config.ProviderBaidu, Key: "bd-secret"},
	}
	if err := store.SaveProviderKeys(toSave); err != nil {
		t.Fatalf("SaveProviderKeys failed: %v", err)
	}

	loaded := []config.Provider{
		{ID: "openai", Kind: config.ProviderOpenAI},
		{ID: "azure", Kind: config.ProviderAzure},
		{ID: "baidu", Kind: config.ProviderBaidu},
	}
	migrated, err := store.LoadProviderKeys(loaded)
	if err != nil {
		t.Fatalf("LoadProviderKeys failed: %v", err)
	}
	if migrated {
		t.Fatal("expected migrated=false on direct round-trip")
	}
	if loaded[0].Key != "sk-openai" || loaded[1].Key != "az-key" || loaded[2].Key != "bd-secret" {
		t.Fatalf("loaded keys mismatch: %+v", loaded)
	}
}

func TestSaveProviderKeysDeletesEmptyAndCleansDefault(t *testing.T) {
	cur := newMemBackend()
	def := newMemBackend()
	_ = cur.Set("provider.openai", []byte("old-cur"))
	_ = def.Set("provider.openai", []byte("old-def"))
	store := &Store{Current: cur, Default: def, Legacy: newMemBackend()}

	providers := []config.Provider{
		{ID: "openai", Kind: config.ProviderOpenAI, Key: "   "},
		{ID: "missing", Kind: config.ProviderAzure, Key: ""},
	}
	if err := store.SaveProviderKeys(providers); err != nil {
		t.Fatalf("SaveProviderKeys failed: %v", err)
	}
	if _, err := cur.Get("provider.openai"); !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatalf("expected provider.openai deleted from Current, got err=%v", err)
	}
	if _, err := def.Get("provider.openai"); !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatalf("expected provider.openai deleted from Default, got err=%v", err)
	}
}

func TestDeleteProviderKey(t *testing.T) {
	cur := newMemBackend()
	def := newMemBackend()
	_ = cur.Set("provider.p1", []byte("k1"))
	_ = def.Set("provider.p1", []byte("k1"))
	store := &Store{Current: cur, Default: def, Legacy: newMemBackend()}

	if err := store.DeleteProviderKey(""); err == nil {
		t.Fatal("expected error when deleting empty provider id")
	}
	if err := store.DeleteProviderKey("p1"); err != nil {
		t.Fatalf("DeleteProviderKey(p1) failed: %v", err)
	}
	if _, err := cur.Get("provider.p1"); !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatalf("expected p1 removed from Current, got %v", err)
	}
	if _, err := def.Get("provider.p1"); !errors.Is(err, mygo.ErrSecretNotFound) {
		t.Fatalf("expected p1 removed from Default, got %v", err)
	}
	// Idempotent when already missing.
	if err := store.DeleteProviderKey("p1"); err != nil {
		t.Fatalf("DeleteProviderKey idempotent call failed: %v", err)
	}
}

func TestStoredSecretMigrationFromDefaultAndLegacy(t *testing.T) {
	t.Run("from default scope", func(t *testing.T) {
		cur := newMemBackend()
		def := newMemBackend()
		_ = def.Set("provider.openai", []byte("from-default"))
		store := &Store{Current: cur, Default: def, Legacy: newMemBackend()}

		providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
		migrated, err := store.LoadProviderKeys(providers)
		if err != nil {
			t.Fatalf("LoadProviderKeys failed: %v", err)
		}
		if !migrated || providers[0].Key != "from-default" {
			t.Fatalf("migrated=%v key=%q", migrated, providers[0].Key)
		}
		if got, err := cur.Get("provider.openai"); err != nil || string(got) != "from-default" {
			t.Fatalf("secret not copied to Current: got=%q err=%v", got, err)
		}
	})

	t.Run("from legacy app scope deletes legacy", func(t *testing.T) {
		cur := newMemBackend()
		def := newMemBackend()
		leg := newMemBackend()
		_ = leg.Set("provider.openai", []byte("from-legacy-app"))
		store := &Store{Current: cur, Default: def, Legacy: leg}

		providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
		migrated, err := store.LoadProviderKeys(providers)
		if err != nil {
			t.Fatalf("LoadProviderKeys failed: %v", err)
		}
		if !migrated || providers[0].Key != "from-legacy-app" {
			t.Fatalf("migrated=%v key=%q", migrated, providers[0].Key)
		}
		if got, err := cur.Get("provider.openai"); err != nil || string(got) != "from-legacy-app" {
			t.Fatalf("secret not copied to Current: got=%q err=%v", got, err)
		}
		if _, err := leg.Get("provider.openai"); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected legacy secret deleted, got %v", err)
		}
	})
}

func TestLoadProviderKeysMigratesLegacyAPIKeySecret(t *testing.T) {
	t.Run("populates empty openai provider and cleans up", func(t *testing.T) {
		cur := newMemBackend()
		def := newMemBackend()
		_ = cur.Set(LegacyAPIKeySecret, []byte("sk-legacy"))
		_ = def.Set(LegacyAPIKeySecret, []byte("sk-legacy"))
		store := &Store{Current: cur, Default: def, Legacy: newMemBackend()}

		providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
		migrated, err := store.LoadProviderKeys(providers)
		if err != nil {
			t.Fatalf("LoadProviderKeys failed: %v", err)
		}
		if !migrated || providers[0].Key != "sk-legacy" {
			t.Fatalf("migrated=%v key=%q", migrated, providers[0].Key)
		}
		if got, err := cur.Get("provider.openai"); err != nil || string(got) != "sk-legacy" {
			t.Fatalf("migrated key not saved to Current: got=%q err=%v", got, err)
		}
		if _, err := cur.Get(LegacyAPIKeySecret); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected api-key deleted from Current, got %v", err)
		}
		if _, err := def.Get(LegacyAPIKeySecret); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected api-key deleted from Default, got %v", err)
		}
	})

	t.Run("does not overwrite existing openai key and cleans up", func(t *testing.T) {
		cur := newMemBackend()
		def := newMemBackend()
		_ = cur.Set("provider.openai", []byte("sk-current"))
		_ = cur.Set(LegacyAPIKeySecret, []byte("sk-legacy"))
		_ = def.Set(LegacyAPIKeySecret, []byte("sk-legacy"))
		store := &Store{Current: cur, Default: def, Legacy: newMemBackend()}

		providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
		_, err := store.LoadProviderKeys(providers)
		if err != nil {
			t.Fatalf("LoadProviderKeys failed: %v", err)
		}
		if providers[0].Key != "sk-current" {
			t.Fatalf("existing key overwritten: got %q", providers[0].Key)
		}
		if _, err := cur.Get(LegacyAPIKeySecret); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected api-key deleted from Current, got %v", err)
		}
		if _, err := def.Get(LegacyAPIKeySecret); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected api-key deleted from Default, got %v", err)
		}
	})
}

func TestUnsupportedBackendErrorsPropagate(t *testing.T) {
	unsupported := errBackend{err: errors.ErrUnsupported}
	store := &Store{Current: unsupported, Default: unsupported, Legacy: unsupported}
	providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI, Key: "sk"}}

	if _, err := store.LoadProviderKeys(providers); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("LoadProviderKeys err = %v, want ErrUnsupported", err)
	}
	if err := store.SaveProviderKeys(providers); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("SaveProviderKeys err = %v, want ErrUnsupported", err)
	}
	if err := store.DeleteProviderKey("openai"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("DeleteProviderKey err = %v, want ErrUnsupported", err)
	}
	if _, err := store.StoredSecret("provider.openai"); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("StoredSecret err = %v, want ErrUnsupported", err)
	}
}

func TestDefaultStoreReadOnlySmoke(t *testing.T) {
	// Strictly read-only: never writes or deletes in the host keyring.
	_, err := LoadProviderKeys(nil)
	if err != nil && !errors.Is(err, mygo.ErrSecretNotFound) && !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("LoadProviderKeys(nil) unexpected error: %v", err)
	}
}

func TestStoredSecretDefaultSetFailureReturnsNotMigrated(t *testing.T) {
	writeErr := errors.New("keyring write denied")
	cur := newSpyBackend()
	cur.setErr = func(string) error { return writeErr }
	def := newSpyBackend()
	_ = def.mem.Set("provider.openai", []byte("sk-from-default"))
	store := &Store{Current: cur, Default: def, Legacy: newSpyBackend()}

	providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
	migrated, err := store.LoadProviderKeys(providers)
	if err != nil {
		t.Fatalf("LoadProviderKeys returned unexpected err: %v", err)
	}
	if migrated {
		t.Fatal("expected migrated=false when Current.Set fails during Default migration")
	}
	if providers[0].Key != "sk-from-default" {
		t.Fatalf("providers[0].Key = %q, want %q", providers[0].Key, "sk-from-default")
	}
	if _, getErr := cur.mem.Get("provider.openai"); !errors.Is(getErr, mygo.ErrSecretNotFound) {
		t.Fatalf("expected Current to remain empty, got err=%v", getErr)
	}
}

func TestLegacyDeleteFailurePreventedFromResurrection(t *testing.T) {
	t.Run("SaveProviderKeys clearing key deletes from Legacy", func(t *testing.T) {
		cur := newSpyBackend()
		def := newSpyBackend()
		leg := newSpyBackend()
		_ = leg.mem.Set("provider.openai", []byte("sk-zombie"))
		leg.deleteErr = func(string) error { return errors.New("legacy delete transient failure") }
		store := &Store{Current: cur, Default: def, Legacy: leg}

		providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
		migrated, err := store.LoadProviderKeys(providers)
		if err != nil {
			t.Fatalf("initial LoadProviderKeys failed: %v", err)
		}
		if !migrated || providers[0].Key != "sk-zombie" {
			t.Fatalf("migrated=%v key=%q, want true / sk-zombie", migrated, providers[0].Key)
		}
		if _, err := leg.mem.Get("provider.openai"); err != nil {
			t.Fatalf("expected sk-zombie still in Legacy after delete failure, got %v", err)
		}

		// Subsequent SaveProviderKeys clearing the key must clear Current, Default, and Legacy.
		leg.deleteErr = nil
		if err := store.SaveProviderKeys([]config.Provider{{ID: "openai", Kind: config.ProviderOpenAI, Key: ""}}); err != nil {
			t.Fatalf("SaveProviderKeys(empty) failed: %v", err)
		}
		if _, err := leg.mem.Get("provider.openai"); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected provider.openai deleted from Legacy, got %v", err)
		}

		reloaded := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
		migrated, err = store.LoadProviderKeys(reloaded)
		if err != nil {
			t.Fatalf("reload LoadProviderKeys failed: %v", err)
		}
		if migrated || reloaded[0].Key != "" {
			t.Fatalf("cleared key resurrected: migrated=%v key=%q", migrated, reloaded[0].Key)
		}
	})

	t.Run("DeleteProviderKey deletes from Legacy and propagates non-NotFound errors", func(t *testing.T) {
		cur := newSpyBackend()
		def := newSpyBackend()
		leg := newSpyBackend()
		_ = leg.mem.Set("provider.openai", []byte("sk-leg"))
		store := &Store{Current: cur, Default: def, Legacy: leg}

		if err := store.DeleteProviderKey("openai"); err != nil {
			t.Fatalf("DeleteProviderKey failed: %v", err)
		}
		if _, err := leg.mem.Get("provider.openai"); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected provider.openai deleted from Legacy, got %v", err)
		}

		legErr := errors.New("legacy delete unsupported")
		leg.deleteErr = func(string) error { return legErr }
		if err := store.DeleteProviderKey("openai"); !errors.Is(err, legErr) {
			t.Fatalf("DeleteProviderKey err = %v, want %v", err, legErr)
		}
		if err := store.SaveProviderKeys([]config.Provider{{ID: "openai", Kind: config.ProviderOpenAI, Key: ""}}); !errors.Is(err, legErr) {
			t.Fatalf("SaveProviderKeys(empty) err = %v, want %v", err, legErr)
		}
	})
}

func TestLoadProviderKeysReadOnlyWithoutOpenAIProvider(t *testing.T) {
	cases := []struct {
		name      string
		providers []config.Provider
	}{
		{name: "nil providers", providers: nil},
		{name: "non-OpenAI provider only", providers: []config.Provider{{ID: "azure", Kind: config.ProviderAzure}}},
		{name: "custom ID with ProviderOpenAI", providers: []config.Provider{{ID: "custom", Kind: config.ProviderOpenAI}}},
		{name: "openai ID with non-OpenAI kind", providers: []config.Provider{{ID: "openai", Kind: config.ProviderAzure}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur := newSpyBackend()
			def := newSpyBackend()
			leg := newSpyBackend()
			_ = cur.mem.Set(LegacyAPIKeySecret, []byte("sk-cur-legacy"))
			_ = def.mem.Set(LegacyAPIKeySecret, []byte("sk-def-legacy"))
			_ = leg.mem.Set(LegacyAPIKeySecret, []byte("sk-leg-legacy"))
			store := &Store{Current: cur, Default: def, Legacy: leg}

			migrated, err := store.LoadProviderKeys(tc.providers)
			if err != nil {
				t.Fatalf("LoadProviderKeys failed: %v", err)
			}
			if migrated {
				t.Fatal("expected migrated=false without matching openai provider")
			}
			if len(cur.setCalls) != 0 || len(def.setCalls) != 0 || len(leg.setCalls) != 0 {
				t.Fatalf("expected zero Set mutations, got cur=%v def=%v leg=%v", cur.setCalls, def.setCalls, leg.setCalls)
			}
			if len(cur.deleteCalls) != 0 || len(def.deleteCalls) != 0 || len(leg.deleteCalls) != 0 {
				t.Fatalf("expected zero Delete mutations, got cur=%v def=%v leg=%v", cur.deleteCalls, def.deleteCalls, leg.deleteCalls)
			}
			for _, p := range tc.providers {
				if p.Key != "" {
					t.Fatalf("expected provider %q key empty, got %q", p.ID, p.Key)
				}
			}
		})
	}
}

func TestLoadProviderKeysExistingOpenAIKeyPreservedAndCleansLegacyAPIKey(t *testing.T) {
	cur := newSpyBackend()
	def := newSpyBackend()
	leg := newSpyBackend()
	_ = cur.mem.Set("provider.openai", []byte("sk-existing"))
	_ = cur.mem.Set(LegacyAPIKeySecret, []byte("sk-stale-cur"))
	_ = def.mem.Set(LegacyAPIKeySecret, []byte("sk-stale-def"))
	_ = leg.mem.Set(LegacyAPIKeySecret, []byte("sk-stale-leg"))
	store := &Store{Current: cur, Default: def, Legacy: leg}

	providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
	migrated, err := store.LoadProviderKeys(providers)
	if err != nil {
		t.Fatalf("LoadProviderKeys failed: %v", err)
	}
	if migrated {
		t.Fatal("expected migrated=false when openai already has a key")
	}
	if providers[0].Key != "sk-existing" {
		t.Fatalf("providers[0].Key = %q, want %q", providers[0].Key, "sk-existing")
	}
	if len(cur.setCalls) != 0 {
		t.Fatalf("expected zero Current.Set calls, got %v", cur.setCalls)
	}
	for name, b := range map[string]*spyBackend{"Current": cur, "Default": def, "Legacy": leg} {
		if _, getErr := b.mem.Get(LegacyAPIKeySecret); !errors.Is(getErr, mygo.ErrSecretNotFound) {
			t.Fatalf("expected %s api-key cleaned up, got err=%v", name, getErr)
		}
	}
}

func TestLoadProviderKeysWhitespaceAndTrimmedLegacyAPIKey(t *testing.T) {
	t.Run("whitespace-only LegacyAPIKeySecret is ignored and cleaned up", func(t *testing.T) {
		cur := newSpyBackend()
		def := newSpyBackend()
		leg := newSpyBackend()
		_ = def.mem.Set(LegacyAPIKeySecret, []byte("   \t\n "))
		_ = leg.mem.Set(LegacyAPIKeySecret, []byte("   "))
		store := &Store{Current: cur, Default: def, Legacy: leg}

		providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
		migrated, err := store.LoadProviderKeys(providers)
		if err != nil {
			t.Fatalf("LoadProviderKeys failed: %v", err)
		}
		if migrated {
			t.Fatal("expected migrated=false for whitespace-only LegacyAPIKeySecret")
		}
		if providers[0].Key != "" {
			t.Fatalf("expected empty Key, got %q", providers[0].Key)
		}
		if len(cur.setCalls) != 0 {
			t.Fatalf("expected zero Current.Set calls, got %v", cur.setCalls)
		}
		if _, err := cur.mem.Get("provider.openai"); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected provider.openai absent in Current, got %v", err)
		}
		for name, b := range map[string]*spyBackend{"Current": cur, "Default": def, "Legacy": leg} {
			if _, err := b.mem.Get(LegacyAPIKeySecret); !errors.Is(err, mygo.ErrSecretNotFound) {
				t.Fatalf("expected %s api-key cleaned up, got err=%v", name, err)
			}
		}
	})

	t.Run("non-empty LegacyAPIKeySecret in Legacy is trimmed and cleaned from all backends", func(t *testing.T) {
		cur := newSpyBackend()
		def := newSpyBackend()
		leg := newSpyBackend()
		_ = leg.mem.Set(LegacyAPIKeySecret, []byte("  sk-trimmed \n"))
		store := &Store{Current: cur, Default: def, Legacy: leg}

		providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
		migrated, err := store.LoadProviderKeys(providers)
		if err != nil {
			t.Fatalf("LoadProviderKeys failed: %v", err)
		}
		if !migrated {
			t.Fatal("expected migrated=true")
		}
		if providers[0].Key != "sk-trimmed" {
			t.Fatalf("providers[0].Key = %q, want %q", providers[0].Key, "sk-trimmed")
		}
		if got, err := cur.mem.Get("provider.openai"); err != nil || string(got) != "sk-trimmed" {
			t.Fatalf("Current provider.openai = %q (err=%v), want sk-trimmed", got, err)
		}
		if _, err := cur.mem.Get(LegacyAPIKeySecret); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected api-key absent in Current, got %v", err)
		}
		if _, err := leg.mem.Get(LegacyAPIKeySecret); !errors.Is(err, mygo.ErrSecretNotFound) {
			t.Fatalf("expected api-key deleted from Legacy, got %v", err)
		}
	})
}

func TestLoadProviderKeysLegacyAPIKeySetFailureDoesNotMutateProvider(t *testing.T) {
	writeErr := errors.New("write denied")
	cur := newSpyBackend()
	cur.setErr = func(key string) error {
		if key == "provider.openai" {
			return writeErr
		}
		return nil
	}
	def := newSpyBackend()
	leg := newSpyBackend()
	_ = leg.mem.Set(LegacyAPIKeySecret, []byte("sk-legacy"))
	store := &Store{Current: cur, Default: def, Legacy: leg}

	providers := []config.Provider{{ID: "openai", Kind: config.ProviderOpenAI}}
	migrated, err := store.LoadProviderKeys(providers)
	if !errors.Is(err, writeErr) {
		t.Fatalf("LoadProviderKeys err = %v, want %v", err, writeErr)
	}
	if migrated {
		t.Fatal("expected migrated=false on Current.Set failure")
	}
	if providers[0].Key != "" {
		t.Fatalf("providers[0].Key mutated to %q despite Set failure", providers[0].Key)
	}
	if got, err := leg.mem.Get(LegacyAPIKeySecret); err != nil || string(got) != "sk-legacy" {
		t.Fatalf("expected Legacy api-key preserved on failure, got %q (err=%v)", got, err)
	}
}

package main

import (
	"io"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"
)

const (
	legacyAppName       = "NextAI Translator"
	legacyAppIdentifier = "com.nextai.translator"
)

// migrateUserData copies the old app's settings and history into the new
// data directory once. The old directory is left in place as a backup.
func migrateUserData() {
	newDir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		return
	}
	appData, err := mygo.App.Path(mygo.PathAppData)
	if err != nil {
		return
	}
	oldDir := filepath.Join(appData, legacyAppName)
	if filepath.Clean(newDir) == filepath.Clean(oldDir) {
		return
	}
	marker := filepath.Join(newDir, ".migrated-from-nextai")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	if _, err := os.Stat(oldDir); err != nil {
		return
	}

	oldConfig := filepath.Join(oldDir, "config.json")
	newConfig := filepath.Join(newDir, "config.json")
	oldHistory := filepath.Join(oldDir, "history.db")
	newHistory := filepath.Join(newDir, "history.db")

	var failed bool
	if _, err := os.Stat(oldConfig); err == nil {
		if !copyIfMissing(oldConfig, newConfig) {
			if info, err := os.Stat(newConfig); err != nil || info.Size() == 0 {
				failed = true
			}
		}
	}
	if _, err := os.Stat(oldHistory); err == nil {
		if !copyIfMissing(oldHistory, newHistory) {
			if info, err := os.Stat(newHistory); err != nil || info.Size() == 0 {
				failed = true
			}
		} else {
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				copyIfMissing(
					filepath.Join(oldDir, "history.db"+suffix),
					filepath.Join(newDir, "history.db"+suffix),
				)
			}
		}
	}
	if !failed {
		_ = os.WriteFile(marker, []byte("migrated\n"), 0o600)
	}
}

// copyIfMissing returns true when it created a non-empty copy. It does not
// replace a non-empty destination.
func copyIfMissing(src, dst string) bool {
	if info, err := os.Stat(dst); err == nil && info.Size() > 0 {
		return false
	}
	in, err := os.Open(src)
	if err != nil {
		return false
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dst)
		return false
	}
	return true
}

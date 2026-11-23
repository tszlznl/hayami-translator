//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godbus/dbus/v5"
)

const (
	portalDest               = "org.freedesktop.portal.Desktop"
	portalPath               = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalRequestIface       = "org.freedesktop.portal.Request"
	portalResponseMember     = "Response"
	portalScreenshotMethod   = "org.freedesktop.portal.Screenshot.Screenshot"
	portalSettingsIface      = "org.freedesktop.portal.Settings"
	portalSettingsReadAll    = "org.freedesktop.portal.Settings.ReadAll"
	portalSettingChanged     = "SettingChanged"
	portalAppearanceNS       = "org.freedesktop.appearance"
	portalSettingsTimeout    = 500 * time.Millisecond
	portalAppearanceCacheTTL = 2 * time.Second
)

func init() {
	readPortalAppearanceHook = readPortalAppearanceCached
}

type portalClient interface {
	Screenshot(ctx context.Context, interactive bool) ([]byte, bool, error)
	AppearanceSettings(ctx context.Context) (PortalAppearance, error)
}

type dbusPortalClient struct{}

var (
	defaultPortalClient portalClient = dbusPortalClient{}
	portalTokenSeq      atomic.Uint64

	portalAppearanceMu struct {
		mu        sync.Mutex
		checkedAt time.Time
		value     PortalAppearance
		err       error
		valid     bool
	}
	portalWatchOnce sync.Once
)

func ensurePortalSettingWatch() {
	portalWatchOnce.Do(func() {
		go watchPortalSettingChanged()
	})
}

func watchPortalSettingChanged() {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return
	}
	defer conn.Close()

	matchOpts := []dbus.MatchOption{
		dbus.WithMatchObjectPath(portalPath),
		dbus.WithMatchInterface(portalSettingsIface),
		dbus.WithMatchMember(portalSettingChanged),
	}
	if err := conn.AddMatchSignal(matchOpts...); err != nil {
		return
	}
	sigCh := make(chan *dbus.Signal, 16)
	conn.Signal(sigCh)
	for sig := range sigCh {
		if sig == nil || sig.Name != portalSettingsIface+"."+portalSettingChanged || len(sig.Body) < 3 {
			continue
		}
		ns, _ := sig.Body[0].(string)
		key, _ := sig.Body[1].(string)
		if ns != portalAppearanceNS {
			continue
		}
		portalAppearanceMu.mu.Lock()
		switch key {
		case "color-scheme":
			if cs, ok := unwrapDBusVariant(sig.Body[2]).(uint32); ok && (cs == 1 || cs == 2) {
				portalAppearanceMu.value.ColorScheme = cs
				portalAppearanceMu.value.HasColorScheme = true
				portalAppearanceMu.checkedAt = time.Now()
				portalAppearanceMu.valid = true
				portalAppearanceMu.err = nil
			} else {
				portalAppearanceMu.valid = false
			}
		case "accent-color":
			if c, ok := parsePortalAccentColor(sig.Body[2]); ok {
				portalAppearanceMu.value.AccentColor = c
				portalAppearanceMu.value.HasAccent = true
				portalAppearanceMu.checkedAt = time.Now()
				portalAppearanceMu.valid = true
				portalAppearanceMu.err = nil
			} else {
				portalAppearanceMu.valid = false
			}
		default:
			portalAppearanceMu.valid = false
		}
		portalAppearanceMu.mu.Unlock()
		if portalAppearanceChangedHook != nil {
			portalAppearanceChangedHook()
		}
	}
}

func captureScreenshotWithPortal(ctx context.Context) ([]byte, error) {
	data, cancelled, err := defaultPortalClient.Screenshot(ctx, true)
	if cancelled {
		return nil, context.Canceled
	}
	return data, err
}

func (dbusPortalClient) Screenshot(ctx context.Context, interactive bool) ([]byte, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, false, fmt.Errorf("connect session dbus: %w", err)
	}
	defer conn.Close()

	matchOpts := []dbus.MatchOption{
		dbus.WithMatchSender(portalDest),
		dbus.WithMatchInterface(portalRequestIface),
		dbus.WithMatchMember(portalResponseMember),
	}
	if err := conn.AddMatchSignalContext(ctx, matchOpts...); err != nil {
		return nil, false, fmt.Errorf("subscribe portal response signal: %w", err)
	}
	defer func() {
		_ = conn.RemoveMatchSignal(matchOpts...)
	}()

	sigCh := make(chan *dbus.Signal, 16)
	conn.Signal(sigCh)
	defer conn.RemoveSignal(sigCh)

	token := fmt.Sprintf("hayami_%d_%d", os.Getpid(), portalTokenSeq.Add(1))
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"interactive":  dbus.MakeVariant(interactive),
	}

	var reqPath dbus.ObjectPath
	obj := conn.Object(portalDest, portalPath)
	if err := obj.CallWithContext(ctx, portalScreenshotMethod, 0, "", options).Store(&reqPath); err != nil {
		return nil, false, fmt.Errorf("portal Screenshot call failed: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			_ = conn.Object(portalDest, reqPath).CallWithContext(closeCtx, portalRequestIface+".Close", 0).Err
			closeCancel()
			return nil, false, ctx.Err()
		case sig, ok := <-sigCh:
			if !ok {
				return nil, false, errors.New("portal signal channel closed")
			}
			if sig == nil || sig.Path != reqPath || sig.Name != portalRequestIface+"."+portalResponseMember {
				continue
			}
			uri, cancelled, err := parseScreenshotResponse(sig.Body)
			if cancelled {
				return nil, true, context.Canceled
			}
			if err != nil {
				return nil, false, err
			}
			data, err := readScreenshotFileURI(uri)
			if err != nil {
				return nil, false, err
			}
			return data, false, nil
		}
	}
}

func (dbusPortalClient) AppearanceSettings(ctx context.Context) (PortalAppearance, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, portalSettingsTimeout)
		defer cancel()
	}

	conn, err := dbus.SessionBus()
	if err != nil {
		return PortalAppearance{}, fmt.Errorf("connect session dbus: %w", err)
	}

	var all map[string]map[string]dbus.Variant
	obj := conn.Object(portalDest, portalPath)
	if err := obj.CallWithContext(ctx, portalSettingsReadAll, 0, []string{portalAppearanceNS}).Store(&all); err != nil {
		return PortalAppearance{}, fmt.Errorf("portal Settings.ReadAll failed: %w", err)
	}
	return parsePortalAppearance(all[portalAppearanceNS]), nil
}

func readPortalAppearanceCached(ctx context.Context) (PortalAppearance, error) {
	ensurePortalSettingWatch()
	now := time.Now()
	portalAppearanceMu.mu.Lock()
	if portalAppearanceMu.valid && now.Sub(portalAppearanceMu.checkedAt) < portalAppearanceCacheTTL {
		val := portalAppearanceMu.value
		err := portalAppearanceMu.err
		portalAppearanceMu.mu.Unlock()
		return val, err
	}
	portalAppearanceMu.mu.Unlock()

	val, err := defaultPortalClient.AppearanceSettings(ctx)

	portalAppearanceMu.mu.Lock()
	portalAppearanceMu.checkedAt = time.Now()
	portalAppearanceMu.value = val
	portalAppearanceMu.err = err
	portalAppearanceMu.valid = true
	portalAppearanceMu.mu.Unlock()

	return val, err
}

func invalidatePortalAppearanceCache() {
	portalAppearanceMu.mu.Lock()
	portalAppearanceMu.valid = false
	portalAppearanceMu.checkedAt = time.Time{}
	portalAppearanceMu.value = PortalAppearance{}
	portalAppearanceMu.err = nil
	portalAppearanceMu.mu.Unlock()
}

func parseScreenshotResponse(body []any) (uri string, cancelled bool, err error) {
	if len(body) < 2 {
		return "", false, fmt.Errorf("unexpected portal response body length %d", len(body))
	}
	code, ok := body[0].(uint32)
	if !ok {
		return "", false, fmt.Errorf("unexpected portal response code type %T", body[0])
	}
	switch code {
	case 0:
	case 1:
		return "", true, nil
	default:
		return "", false, fmt.Errorf("portal screenshot returned error code %d", code)
	}

	results, ok := body[1].(map[string]dbus.Variant)
	if !ok {
		return "", false, fmt.Errorf("unexpected portal response results type %T", body[1])
	}
	rawURI, ok := results["uri"]
	if !ok {
		return "", false, errors.New("portal screenshot response missing uri")
	}
	uri, ok = unwrapDBusVariant(rawURI).(string)
	if !ok || strings.TrimSpace(uri) == "" {
		return "", false, errors.New("portal screenshot response uri is empty or invalid")
	}
	return strings.TrimSpace(uri), false, nil
}

func parseFileURI(rawURI string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return "", fmt.Errorf("invalid screenshot uri %q: %w", rawURI, err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported screenshot uri scheme %q", u.Scheme)
	}
	if u.Host != "" && u.Host != "localhost" {
		return "", fmt.Errorf("unsupported screenshot uri host %q", u.Host)
	}
	if u.Path == "" {
		return "", errors.New("empty screenshot file path")
	}
	return filepath.FromSlash(u.Path), nil
}

func isTempScreenshotPath(path string) bool {
	if path == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return false
	}
	tempDir := filepath.Clean(os.TempDir())
	resolvedTemp, err := filepath.EvalSymlinks(tempDir)
	if err != nil {
		resolvedTemp = tempDir
	}
	resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(cleanPath))
	if err != nil {
		resolvedDir = filepath.Dir(cleanPath)
	}
	resolvedPath := filepath.Join(resolvedDir, filepath.Base(cleanPath))
	rel, err := filepath.Rel(resolvedTemp, resolvedPath)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readScreenshotFileURI(rawURI string) ([]byte, error) {
	path, err := parseFileURI(rawURI)
	if err != nil {
		return nil, err
	}
	data, readErr := os.ReadFile(path)
	if isTempScreenshotPath(path) {
		_ = os.Remove(path)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read portal screenshot %q: %w", path, readErr)
	}
	if len(data) == 0 {
		return nil, errors.New("portal screenshot file is empty")
	}
	return data, nil
}

func unwrapDBusVariant(v any) any {
	for {
		dv, ok := v.(dbus.Variant)
		if !ok {
			return v
		}
		v = dv.Value()
	}
}

func parsePortalAppearance(attrs map[string]dbus.Variant) PortalAppearance {
	var out PortalAppearance
	if len(attrs) == 0 {
		return out
	}
	if csVar, ok := attrs["color-scheme"]; ok {
		if cs, ok := unwrapDBusVariant(csVar).(uint32); ok && (cs == 1 || cs == 2) {
			out.ColorScheme = cs
			out.HasColorScheme = true
		}
	}
	if acVar, ok := attrs["accent-color"]; ok {
		if c, ok := parsePortalAccentColor(acVar); ok {
			out.AccentColor = c
			out.HasAccent = true
		}
	}
	return out
}

func parsePortalAccentColor(v any) (ui.Color, bool) {
	v = unwrapDBusVariant(v)
	var r, g, b float64
	switch val := v.(type) {
	case []any:
		if len(val) != 3 {
			return ui.Color{}, false
		}
		var ok1, ok2, ok3 bool
		r, ok1 = val[0].(float64)
		g, ok2 = val[1].(float64)
		b, ok3 = val[2].(float64)
		if !ok1 || !ok2 || !ok3 {
			return ui.Color{}, false
		}
	case []float64:
		if len(val) != 3 {
			return ui.Color{}, false
		}
		r, g, b = val[0], val[1], val[2]
	case struct{ R, G, B float64 }:
		r, g, b = val.R, val.G, val.B
	default:
		return ui.Color{}, false
	}

	for _, c := range [3]float64{r, g, b} {
		if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 || c > 1 {
			return ui.Color{}, false
		}
	}
	return ui.RGB(
		uint8(r*255+0.5),
		uint8(g*255+0.5),
		uint8(b*255+0.5),
	), true
}

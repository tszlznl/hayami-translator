package main

import (
	"errors"
	"testing"

	"github.com/egoist/mygo"
)

type fakeTray struct {
	menu *mygo.Menu
}

func (f *fakeTray) SetMenu(menu *mygo.Menu) { f.menu = menu }
func (f *fakeTray) Destroy()                {}

func TestUpdateOpenAtLoginSyncsTrayMenu(t *testing.T) {
	tray := &fakeTray{}
	a := &app{tray: tray}
	var applied bool
	err := a.updateOpenAtLogin(true, func(enabled bool) error {
		applied = enabled
		return nil
	})
	if err != nil {
		t.Fatalf("updateOpenAtLogin returned %v", err)
	}
	if !applied || !a.openAtLogin {
		t.Fatal("successful update must apply and store open-at-login")
	}
	if tray.menu == nil {
		t.Fatal("tray menu must be rebuilt after a successful update")
	}
	item := tray.menu.ItemByID("login")
	if item == nil || !item.Checked {
		t.Fatal("tray login item must reflect the new open-at-login state")
	}
}

func TestUpdateOpenAtLoginKeepsStateWhenSystemUpdateFails(t *testing.T) {
	tray := &fakeTray{}
	a := &app{tray: tray}
	a.refreshTrayMenu()
	wantErr := errors.New("registry denied")
	err := a.updateOpenAtLogin(true, func(bool) error { return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatalf("updateOpenAtLogin returned %v, want %v", err, wantErr)
	}
	if a.openAtLogin {
		t.Fatal("failed system update must not change app state")
	}
	item := tray.menu.ItemByID("login")
	if item == nil || item.Checked {
		t.Fatal("failed system update must leave the tray item unchecked")
	}
}

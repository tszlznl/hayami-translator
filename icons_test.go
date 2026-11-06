package main

import (
	"context"
	"io"
	"os/exec"
	"reflect"
	"runtime"
	"testing"
)

func TestLinuxSpeechCommandUsesStdin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux speech command")
	}
	text := "引号 ' \"\n$(touch /never-run); --voices <speak>literal text</speak>"
	cmd, err := speechCommand(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"espeak-ng", "--stdin"}; !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("args = %q, want %q", cmd.Args, want)
	}
	if cmd.Stdin == nil {
		t.Fatal("speech text must be sent through stdin")
	}
	input, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if string(input) != text {
		t.Fatalf("stdin = %q, want %q", input, text)
	}
}

func TestSpeechPlaybackStopsBeforeCommandStarts(t *testing.T) {
	var playback speechPlayback
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "powershell.exe")
	playback.begin(cmd, cancel)

	if !playback.active() {
		t.Fatal("a started playback must report active before its process starts")
	}
	playback.stop()
	if playback.active() {
		t.Fatal("stop must clear the playback state immediately")
	}
	if ctx.Err() == nil {
		t.Fatal("stop must cancel the command context before the process starts")
	}
}

func TestSpeechPlaybackIgnoresStaleCompletion(t *testing.T) {
	var playback speechPlayback
	oldCmd := exec.Command("old")
	oldCtx, oldCancel := context.WithCancel(context.Background())
	playback.begin(oldCmd, oldCancel)

	newCmd := exec.Command("new")
	newCtx, newCancel := context.WithCancel(context.Background())
	playback.begin(newCmd, newCancel)

	playback.done(oldCmd)
	if !playback.active() {
		t.Fatal("an older command finishing must not clear the current playback")
	}
	if oldCtx.Err() == nil {
		t.Fatal("finishing the old command must release its context")
	}

	playback.done(newCmd)
	if playback.active() {
		t.Fatal("finishing the current command must clear the playback")
	}
	if newCtx.Err() == nil {
		t.Fatal("finishing the current command must release its context")
	}
}

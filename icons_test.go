package main

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestLinuxSpeechCommandUsesStdin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux speech command")
	}
	if _, err := exec.LookPath("espeak-ng"); err != nil {
		t.Skip("espeak-ng not installed")
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

func TestLinuxSpeechCommandChineseAndMissingBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux speech command")
	}
	if _, err := exec.LookPath("espeak-ng"); err == nil {
		cmd, err := speechCommand(context.Background(), "你好，世界")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"espeak-ng", "-v", "cmn", "--stdin"}; !reflect.DeepEqual(cmd.Args, want) {
			t.Fatalf("chinese args = %q, want %q", cmd.Args, want)
		}
	}
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))
	_, err := speechCommand(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "espeak-ng") {
		t.Fatalf("expected missing espeak-ng error, got %v", err)
	}
}

func TestSpeechPlaybackStopsBeforeCommandStarts(t *testing.T) {
	var playback speechPlayback
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "espeak-ng")
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

func TestDetectSpeechVoiceAndAudioValidation(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"Good morning", ""},
		{"你好，世界", "cmn"},
		{"こんにちは世界", "ja"},
		{"안녕하세요", "ko"},
		{"Привет мир", "ru"},
		{"مرحبا", "ar"},
		{"สวัสดี", "th"},
	}
	for _, tc := range cases {
		if got := detectSpeechVoice(tc.text); got != tc.want {
			t.Errorf("detectSpeechVoice(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}

	if isValidAudioPayload([]byte("corrupt-audio-bytes"), "mp3") {
		t.Fatal("corrupt bytes must not pass MP3 validation")
	}
	if isValidAudioPayload([]byte("corrupt-audio-bytes"), "wav") {
		t.Fatal("corrupt bytes must not pass WAV validation")
	}
	if !isValidAudioPayload([]byte{0xff, 0xfb, 0x10, 0xc0}, "mp3") {
		t.Fatal("valid MPEG-1 Layer III frame sync must pass MP3 validation")
	}
}

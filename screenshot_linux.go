//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var portalScreenshotFunc = captureScreenshotWithPortal

func captureScreenshot(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if portalScreenshotFunc != nil {
		img, err := portalScreenshotFunc(ctx)
		if err == nil && len(img) > 0 {
			return img, nil
		}
		if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil) {
			return nil, err
		}
	}
	file, err := os.CreateTemp("", "hayami-screenshot-*.png")
	if err != nil {
		return nil, fmt.Errorf("创建截图临时文件失败: %w", err)
	}
	defer os.Remove(file.Name())
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("关闭截图临时文件失败: %w", err)
	}

	if err := runLinuxScreenshotTool(ctx, file.Name()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	image, err := os.ReadFile(file.Name())
	if os.IsNotExist(err) {
		return nil, context.Canceled
	}
	if err != nil {
		return nil, fmt.Errorf("读取截图失败: %w", err)
	}
	// Escape closes interactive screenshot tools cleanly without writing an image.
	if len(image) == 0 {
		return nil, context.Canceled
	}
	return image, nil
}

func runLinuxScreenshotTool(ctx context.Context, outPath string) error {
	// Prefer KDE Spectacle when installed; fall back to common Wayland/X11 tools
	// so GNOME, wlroots/Hyprland/Sway and X11 desktops also work out of the box.
	if _, err := exec.LookPath("spectacle"); err == nil {
		// A separate instance must finish saving before we read or remove its file.
		cmd := exec.CommandContext(ctx, "spectacle", "--region", "--background", "--nonotify", "--new-instance", "--output", outPath)
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("Spectacle 区域截图失败: %w", err)
		}
		return nil
	}
	if _, err := exec.LookPath("grim"); err == nil {
		if _, err := exec.LookPath("slurp"); err == nil {
			regionOut, err := exec.CommandContext(ctx, "slurp").Output()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// slurp exits non-zero when cancelled with Escape.
				if strings.TrimSpace(string(regionOut)) == "" {
					return context.Canceled
				}
				return fmt.Errorf("slurp 选区失败: %w", err)
			}
			region := strings.TrimSpace(string(regionOut))
			if region == "" {
				return context.Canceled
			}
			if err := exec.CommandContext(ctx, "grim", "-g", region, outPath).Run(); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("grim 区域截图失败: %w", err)
			}
			return nil
		}
	}
	if _, err := exec.LookPath("gnome-screenshot"); err == nil {
		if err := exec.CommandContext(ctx, "gnome-screenshot", "-a", "-f", outPath).Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("gnome-screenshot 区域截图失败: %w", err)
		}
		return nil
	}
	if _, err := exec.LookPath("flameshot"); err == nil {
		out, err := exec.CommandContext(ctx, "flameshot", "gui", "--raw").Output()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("Flameshot 区域截图失败: %w", err)
		}
		if len(out) == 0 {
			return context.Canceled
		}
		return os.WriteFile(outPath, out, 0o600)
	}
	if _, err := exec.LookPath("xfce4-screenshooter"); err == nil {
		if err := exec.CommandContext(ctx, "xfce4-screenshooter", "-r", "-s", outPath).Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("xfce4-screenshooter 区域截图失败: %w", err)
		}
		return nil
	}
	if _, err := exec.LookPath("scrot"); err == nil {
		if err := exec.CommandContext(ctx, "scrot", "-s", "-o", outPath).Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("scrot 区域截图失败: %w", err)
		}
		return nil
	}
	return fmt.Errorf("未找到可用的区域截图工具，请安装 spectacle、grim+slurp、gnome-screenshot、flameshot 或 scrot")
}

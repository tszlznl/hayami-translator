# Wayland Selection Feasibility Evaluation

## Executive Summary

Implementing native pure-Go (`CGO_ENABLED=0`) Wayland PRIMARY selection monitoring inside Hayami Translator is technically feasible only for compositors exposing privileged data-control protocols (`ext_data_control_v1` / `zwlr_data_control_manager_v1`), requires maintaining a custom ~800–1,500 LOC Wayland binary wire-protocol and `SCM_RIGHTS` file-descriptor stack, and **still fails in the background on GNOME Mutter** (which intentionally refuses data-control protocols to unprivileged clients). Meanwhile, the standard unprivileged protocol (`zwp_primary_selection_v1`) only emits selection offers to the surface currently holding keyboard focus, making background cross-application selection capture impossible.

Accordingly, for Milestone M2 we **defer native pure-Go Wayland wire-protocol bindings**, retain `wl-paste --primary --watch` (plus X11 `xclip`/`xsel` and `pollSelection` fallbacks), and harden the subprocess watcher lifecycle with deterministic unit tests.

---

## 1. Protocol Landscape

### 1.1 `zwp_primary_selection_v1` (Unprivileged Standard)
- Defined in `wp_primary_selection_unstable_v1`.
- Selection events (`zwp_primary_selection_device_v1.selection(offer)`) are dispatched **only to the Wayland client whose `wl_surface` holds keyboard focus** (`wl_keyboard.enter`).
- When focus moves to another application, the compositor sends a `NULL` offer to the unfocused client.
- **Implication for Hayami**: Hayami operates as a background tray/panel translator while the user highlights text in external applications (browsers, editors, PDF readers, terminals). A direct `zwp_primary_selection_v1` client receives **zero** selection events while external windows have focus. Polling via a hidden focus-stealing surface would cause focus flicker, dismiss context menus/selections in active apps, and reintroduce idle wakeups.

### 1.2 Background Data-Control Protocols (`ext_data_control_v1` & `zwlr_data_control_manager_v1`)
- Unprivileged clients cannot read selections in the background; compositors that permit background clipboard/PRIMARY managers expose a separate privileged global:
  - **`ext_data_control_v1`** (`ext_data_control_manager_v1` -> `get_data_device(seat)` -> `primary_selection(offer)`): cross-compositor Wayland staging protocol supported by modern KWin and wlroots/Smithay compositors.
  - **`zwlr_data_control_manager_v1`**: wlroots predecessor protocol supported across Sway, Hyprland, River, Wayfire, Labwc, and transitional KWin builds.
- Both protocols emit `primary_selection` offers immediately when any window updates the PRIMARY selection, enabling event-driven `0%` idle CPU watching.

### 1.3 Compositor Support Matrix

| Compositor / Desktop Environment | `zwp_primary_selection_v1` | `zwlr_data_control_manager_v1` | `ext_data_control_v1` | Background Watch Feasibility |
|---|---|---|---|---|
| **KDE KWin (Plasma 6 Wayland)** | Yes | Transitional | Yes | **Works** (event-driven, zero idle wakeups) |
| **wlroots (Sway, Hyprland, River, Wayfire, Labwc) & Smithay / Niri** | Yes | Yes | Yes (recent releases) | **Works** (event-driven, zero idle wakeups) |
| **GNOME Mutter** | Yes (+ `gtk_primary_selection`) | **No** (declined by design) | **No** (declined by design) | **Blocked** for unprivileged Wayland clients; requires GNOME Shell extension, XWayland (`xclip`/`xsel`), or explicit hotkey (`Alt+F`) |

---

## 2. Pure-Go (`CGO_ENABLED=0`) Implementation Requirements

Because Hayami maintains strict `CGO_ENABLED=0` pure-Go builds (`scripts/check.sh`, ADR `D14`), linking `libwayland-client.so.0` via CGO is ruled out. Replacing `wl-paste` with an in-process pure-Go Wayland client would require implementing and maintaining:

1. **Socket Discovery & Lifecycle**:
   - Resolve `$WAYLAND_DISPLAY` (relative names joined with `$XDG_RUNTIME_DIR`, defaulting to `wayland-0`), connect over `AF_UNIX` `SOCK_STREAM`, and handle `EPIPE`/`ECONNRESET` reconnect backoff on compositor restarts.
2. **Wayland Binary Wire Framing**:
   - Little-endian 8-byte message headers (`object_id uint32`, `(size_bytes << 16) | opcode uint32`, 4-byte aligned payload), length-prefixed NUL-terminated padded strings, padded arrays, and `new_id` argument serialization for `wl_registry.bind` (`name`, `interface`, `version`, `new_id`).
3. **Object Registry & ID Recycling**:
   - Client-side object ID allocation starting at `2` (`1` = `wl_display`), `wl_display.get_registry` + `wl_display.sync` roundtrip, tracking `wl_registry.global` / `global_remove`, handling fatal `wl_display.error` and `wl_display.delete_id`, binding all `wl_seat` globals, and negotiating `ext_data_control_manager_v1` vs `zwlr_data_control_manager_v1`.
4. **`SCM_RIGHTS` File-Descriptor Passing**:
   - Create `O_CLOEXEC` pipes via `syscall.Pipe2`, attach the write FD as ancillary control data (`syscall.UnixRights(writeFD)` via `syscall.Sendmsg`) on `receive(mime_type, fd)`, close the local write FD immediately after `Sendmsg`, and drain any inbound FDs via `syscall.Recvmsg` + `syscall.ParseSocketControlMessage` + `syscall.ParseUnixRights` to prevent `EMFILE` descriptor exhaustion.
5. **MIME Negotiation & Non-Blocking Pipe Reads**:
   - Buffer `data_offer.offer(mime_type)` events until `primary_selection(offer_id)` arrives; rank `text/plain;charset=utf-8` > `UTF8_STRING` > `text/plain` > `STRING`; ignore non-text MIME types; destroy superseded offers; and read the pipe off the protocol loop with strict timeouts (`1s`) and size caps so a stalled selection owner cannot deadlock the Wayland event stream.

---

## 3. Side-by-Side Comparison

| Dimension | `wl-paste --primary --watch` + `xclip`/`xsel` (Current Hardened) | Native Pure-Go Wayland Protocol Client |
|---|---|---|
| **Protocol coverage** | `ext_data_control_v1`, `zwlr_data_control_manager_v1`, `zwp_primary_selection_v1`, `gtk_primary_selection` (`wl-clipboard` 2.3.0) | Custom implementation needed for each protocol revision |
| **GNOME Mutter background watch** | Falls back cleanly after 3 watcher exits to `pollSelection` / X11 / hotkey | Still blocked server-side on Mutter (needs same fallback anyway) |
| **Idle CPU usage (KWin / wlroots)** | **0%** (event-driven child process blocked in Wayland poll) | **0%** (in-process Unix socket poll) |
| **Maintenance & risk surface** | ~200 LOC Go process supervisor (`Setpgid`, `WaitDelay`, bounded stderr, pipe drain) | ~800–1,500 LOC binary wire parser, `SCM_RIGHTS` FD lifecycle, multi-seat edge cases |
| **`CGO_ENABLED=0` compatibility** | 100% pure Go | 100% pure Go |

---

## 4. Decision & Hardening Implemented in M2

1. **Decision**: Defer native pure-Go Wayland wire-protocol bindings (`docs/decisions.md` **D15**). Retain `wl-paste --primary --watch` on Wayland, `xclip`/`xsel` on X11, and `pollSelection` fallback.
2. **Hardening Applied**:
   - Fixed the `StdoutPipe` + `cmd.Wait()` contract race on `ctx.Done()` in `selection_hook_linux.go` by draining `<-scanDone` (with a bounded fallback close) before calling `Wait()`.
   - Added process-group isolation (`SysProcAttr{Setpgid: true}` + `-Pid` `SIGKILL` cancel) and `WaitDelay = 200ms` across both `selection_hook_linux.go` and `selection_linux.go` so hung child descendants never block shutdown.
   - Added bounded stderr capture (`4KB`) on the watcher process so data-control failures (e.g., GNOME Mutter `"Watch mode requires a compositor that supports the data-control protocol"`) surface clearly in `debug.log`.
   - Added a `30s` `stableThreshold` uptime reset on the watcher failure counter so rare transient exits over long sessions do not permanently degrade to polling.
   - Factored `selectionEnv`, `watchProcess`, `watchCmdFactory`, and `selectionHookDeps` for deterministic unit testing under `go test -race` without spawning real display servers.

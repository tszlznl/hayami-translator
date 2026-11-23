//go:build e2e && linux

package e2e_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	portalBusName        = "org.freedesktop.portal.Desktop"
	portalObjectPath     = "/org/freedesktop/portal/desktop"
	portalScreenshotIf   = "org.freedesktop.portal.Screenshot"
	portalSettingsIf     = "org.freedesktop.portal.Settings"
	portalRequestIf      = "org.freedesktop.portal.Request"
	portalNotFoundErr    = "org.freedesktop.portal.Error.NotFound"
	portalAppearanceNS   = "org.freedesktop.appearance"
	portalColorSchemeKey = "color-scheme"
	portalAccentColorKey = "accent-color"
	portalContrastKey    = "contrast"

	gDBusConnFlagsAuthClientMessageBus = uint32(1 | 8)
	gBusNameOwnerFlagsDoNotQueue       = uint32(4)
)

const portalIntrospectionXML = `<node>
  <interface name="org.freedesktop.portal.Screenshot">
    <method name="Screenshot">
      <arg type="s" name="parent_window" direction="in"/>
      <arg type="a{sv}" name="options" direction="in"/>
      <arg type="o" name="handle" direction="out"/>
    </method>
    <method name="PickColor">
      <arg type="s" name="parent_window" direction="in"/>
      <arg type="a{sv}" name="options" direction="in"/>
      <arg type="o" name="handle" direction="out"/>
    </method>
    <property name="AvailableTargets" type="u" access="read"/>
    <property name="version" type="u" access="read"/>
  </interface>
  <interface name="org.freedesktop.portal.Settings">
    <method name="ReadAll">
      <arg type="as" name="namespaces" direction="in"/>
      <arg type="a{sa{sv}}" name="value" direction="out"/>
    </method>
    <method name="Read">
      <arg type="s" name="namespace" direction="in"/>
      <arg type="s" name="key" direction="in"/>
      <arg type="v" name="value" direction="out"/>
    </method>
    <method name="ReadOne">
      <arg type="s" name="namespace" direction="in"/>
      <arg type="s" name="key" direction="in"/>
      <arg type="v" name="value" direction="out"/>
    </method>
    <signal name="SettingChanged">
      <arg type="s" name="namespace"/>
      <arg type="s" name="key"/>
      <arg type="v" name="value"/>
    </signal>
    <property name="version" type="u" access="read"/>
  </interface>
  <interface name="org.freedesktop.portal.Request">
    <method name="Close"/>
    <signal name="Response">
      <arg type="u" name="response"/>
      <arg type="a{sv}" name="results"/>
    </signal>
  </interface>
</node>`

var (
	gioOnce sync.Once
	gioErr  error

	gDBusConnNewForAddressSync func(addr string, flags uint32, observer, cancellable uintptr, gerr *uintptr) uintptr
	gDBusConnSetExitOnClose    func(conn uintptr, exitOnClose bool)
	gDBusConnCloseSync         func(conn, cancellable uintptr, gerr *uintptr) bool
	gDBusConnRegisterObject    func(conn uintptr, path string, ifaceInfo uintptr, vtable unsafe.Pointer, userData, freeNotify uintptr, gerr *uintptr) uint32
	gDBusConnUnregisterObject  func(conn uintptr, regID uint32) bool
	gDBusConnEmitSignal        func(conn, dest uintptr, path, iface, signal string, params uintptr, gerr *uintptr) bool
	gDBusConnCallSync          func(conn uintptr, busName, path, iface, method string, params, replyType uintptr, flags uint32, timeoutMs int32, cancellable uintptr, gerr *uintptr) uintptr
	gDBusConnSignalSubscribe   func(conn uintptr, sender, iface, member, path, arg0 *byte, flags uint32, cb, userData, freeNotify uintptr) uint32
	gDBusConnSignalUnsubscribe func(conn uintptr, subID uint32)
	gDBusNodeInfoNewForXML     func(xml string, gerr *uintptr) uintptr
	gDBusNodeInfoLookupIface   func(node uintptr, name string) uintptr
	gDBusNodeInfoUnref         func(node uintptr)
	gBusOwnNameOnConn          func(conn uintptr, name string, flags uint32, acquired, lost, userData, freeNotify uintptr) uint32
	gBusUnownName              func(ownerID uint32)
	gDBusMethodInvGetSender    func(inv uintptr) uintptr
	gDBusMethodInvReturnValue  func(inv, params uintptr)
	gDBusMethodInvReturnError  func(inv uintptr, name, msg string)

	gErrorFree                func(err uintptr)
	gMainContextNew           func() uintptr
	gMainContextUnref         func(ctx uintptr)
	gMainContextPushThreadDef func(ctx uintptr)
	gMainContextPopThreadDef  func(ctx uintptr)
	gMainContextInvoke        func(ctx, fn, data uintptr)
	gMainLoopNew              func(ctx uintptr, isRunning bool) uintptr
	gMainLoopRun              func(loop uintptr)
	gMainLoopQuit             func(loop uintptr)
	gMainLoopUnref            func(loop uintptr)
	gVariantTypeNew           func(typ string) uintptr
	gVariantUnref             func(v uintptr)
	gVariantNewString         func(s string) uintptr
	gVariantNewObjectPath     func(s string) uintptr
	gVariantNewUint32         func(u uint32) uintptr
	gVariantNewDouble         func(d float64) uintptr
	gVariantNewBoolean        func(b bool) uintptr
	gVariantNewVariant        func(child uintptr) uintptr
	gVariantNewDictEntry      func(k, v uintptr) uintptr
	gVariantNewTuple          func(children unsafe.Pointer, n uintptr) uintptr
	gVariantNewArray          func(elemType uintptr, children unsafe.Pointer, n uintptr) uintptr
	gVariantNChildren         func(v uintptr) uintptr
	gVariantGetChildValue     func(v uintptr, idx uintptr) uintptr
	gVariantLookupValue       func(dict uintptr, key string, expectedType uintptr) uintptr
	gVariantGetTypeString     func(v uintptr) uintptr
	gVariantGetString         func(v, length uintptr) uintptr
	gVariantGetUint32         func(v uintptr) uint32
	gVariantGetDouble         func(v uintptr) float64
	gVariantGetBoolean        func(v uintptr) bool
	gVariantGetVariant        func(v uintptr) uintptr
	gVariantPrint             func(v uintptr, annotate bool) uintptr
	gFree                     func(mem uintptr)

	varTypeS     uintptr
	varTypeSV    uintptr
	varTypeSASV  uintptr
	varTypeSAASV uintptr

	portalVtable [11]uintptr

	portalMethodCallCB   uintptr
	portalGetPropCB      uintptr
	portalNameAcquiredCB uintptr
	portalNameLostCB     uintptr
	portalMainInvokeCB   uintptr
	portalClientSignalCB uintptr

	portalMu       sync.Mutex
	portalNextID   uintptr = 1
	portalRegistry         = map[uintptr]*mockPortal{}
	invokeTasks            = map[uintptr]func(){}
	signalSubs             = map[uintptr]func(sender, path, iface, signal string, params uintptr){}
)

func initPortalGIO() error {
	gioOnce.Do(func() {
		gio, err := purego.Dlopen("libgio-2.0.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			gioErr = fmt.Errorf("dlopen libgio-2.0.so.0: %w", err)
			return
		}
		glib, err := purego.Dlopen("libglib-2.0.so.0", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			gioErr = fmt.Errorf("dlopen libglib-2.0.so.0: %w", err)
			return
		}

		for name, fn := range map[string]any{
			"g_dbus_connection_new_for_address_sync":     &gDBusConnNewForAddressSync,
			"g_dbus_connection_set_exit_on_close":        &gDBusConnSetExitOnClose,
			"g_dbus_connection_close_sync":               &gDBusConnCloseSync,
			"g_dbus_connection_register_object":          &gDBusConnRegisterObject,
			"g_dbus_connection_unregister_object":        &gDBusConnUnregisterObject,
			"g_dbus_connection_emit_signal":              &gDBusConnEmitSignal,
			"g_dbus_connection_call_sync":                &gDBusConnCallSync,
			"g_dbus_connection_signal_subscribe":         &gDBusConnSignalSubscribe,
			"g_dbus_connection_signal_unsubscribe":       &gDBusConnSignalUnsubscribe,
			"g_dbus_node_info_new_for_xml":               &gDBusNodeInfoNewForXML,
			"g_dbus_node_info_lookup_interface":          &gDBusNodeInfoLookupIface,
			"g_dbus_node_info_unref":                     &gDBusNodeInfoUnref,
			"g_bus_own_name_on_connection":               &gBusOwnNameOnConn,
			"g_bus_unown_name":                           &gBusUnownName,
			"g_dbus_method_invocation_get_sender":        &gDBusMethodInvGetSender,
			"g_dbus_method_invocation_return_value":      &gDBusMethodInvReturnValue,
			"g_dbus_method_invocation_return_dbus_error": &gDBusMethodInvReturnError,
		} {
			purego.RegisterLibFunc(fn, gio, name)
		}

		for name, fn := range map[string]any{
			"g_error_free":                       &gErrorFree,
			"g_main_context_new":                 &gMainContextNew,
			"g_main_context_unref":               &gMainContextUnref,
			"g_main_context_push_thread_default": &gMainContextPushThreadDef,
			"g_main_context_pop_thread_default":  &gMainContextPopThreadDef,
			"g_main_context_invoke":              &gMainContextInvoke,
			"g_main_loop_new":                    &gMainLoopNew,
			"g_main_loop_run":                    &gMainLoopRun,
			"g_main_loop_quit":                   &gMainLoopQuit,
			"g_main_loop_unref":                  &gMainLoopUnref,
			"g_variant_type_new":                 &gVariantTypeNew,
			"g_variant_unref":                    &gVariantUnref,
			"g_variant_new_string":               &gVariantNewString,
			"g_variant_new_object_path":          &gVariantNewObjectPath,
			"g_variant_new_uint32":               &gVariantNewUint32,
			"g_variant_new_double":               &gVariantNewDouble,
			"g_variant_new_boolean":              &gVariantNewBoolean,
			"g_variant_new_variant":              &gVariantNewVariant,
			"g_variant_new_dict_entry":           &gVariantNewDictEntry,
			"g_variant_new_tuple":                &gVariantNewTuple,
			"g_variant_new_array":                &gVariantNewArray,
			"g_variant_n_children":               &gVariantNChildren,
			"g_variant_get_child_value":          &gVariantGetChildValue,
			"g_variant_lookup_value":             &gVariantLookupValue,
			"g_variant_get_type_string":          &gVariantGetTypeString,
			"g_variant_get_string":               &gVariantGetString,
			"g_variant_get_uint32":               &gVariantGetUint32,
			"g_variant_get_double":               &gVariantGetDouble,
			"g_variant_get_boolean":              &gVariantGetBoolean,
			"g_variant_get_variant":              &gVariantGetVariant,
			"g_variant_print":                    &gVariantPrint,
			"g_free":                             &gFree,
		} {
			purego.RegisterLibFunc(fn, glib, name)
		}

		varTypeS = gVariantTypeNew("s")
		varTypeSV = gVariantTypeNew("{sv}")
		varTypeSASV = gVariantTypeNew("a{sv}")
		varTypeSAASV = gVariantTypeNew("{sa{sv}}")

		portalMethodCallCB = purego.NewCallback(portalMethodCallTrampoline)
		portalGetPropCB = purego.NewCallback(portalGetPropTrampoline)
		portalNameAcquiredCB = purego.NewCallback(portalNameAcquiredTrampoline)
		portalNameLostCB = purego.NewCallback(portalNameLostTrampoline)
		portalMainInvokeCB = purego.NewCallback(portalMainInvokeTrampoline)
		portalClientSignalCB = purego.NewCallback(portalClientSignalTrampoline)

		portalVtable[0] = portalMethodCallCB
		portalVtable[1] = portalGetPropCB
	})
	return gioErr
}

func cStrPtr(s string) *byte {
	if s == "" {
		return nil
	}
	b := make([]byte, len(s)+1)
	copy(b, s)
	return &b[0]
}

func cStr(p uintptr) string {
	if p == 0 {
		return ""
	}
	base := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	var b []byte
	for c := (*byte)(base); *c != 0; c = (*byte)(unsafe.Add(unsafe.Pointer(c), 1)) {
		b = append(b, *c)
	}
	return string(b)
}

func consumeGError(gerr uintptr) string {
	if gerr == 0 {
		return ""
	}
	msgPtr := *(*uintptr)(unsafe.Add(*(*unsafe.Pointer)(unsafe.Pointer(&gerr)), 8))
	msg := cStr(msgPtr)
	gErrorFree(gerr)
	return msg
}

func variantTuple(elems ...uintptr) uintptr {
	return gVariantNewTuple(unsafe.Pointer(&elems[0]), uintptr(len(elems)))
}

func variantEmptyDictSV() uintptr {
	return gVariantNewArray(varTypeSV, nil, 0)
}

func allocPortalID() uintptr {
	portalMu.Lock()
	defer portalMu.Unlock()
	id := portalNextID
	portalNextID++
	return id
}

func lookupPortal(id uintptr) *mockPortal {
	portalMu.Lock()
	defer portalMu.Unlock()
	return portalRegistry[id]
}

func portalMethodCallTrampoline(conn, sender, objPath, iface, method, params, invocation, userData uintptr) {
	p := lookupPortal(userData)
	if p == nil {
		gDBusMethodInvReturnError(invocation, "org.freedesktop.DBus.Error.Failed", "portal instance closed")
		return
	}
	p.handleMethodCall(
		cStr(sender),
		cStr(objPath),
		cStr(iface),
		cStr(method),
		params,
		invocation,
	)
}

func portalGetPropTrampoline(conn, sender, objPath, iface, prop, gerr, userData uintptr) uintptr {
	p := lookupPortal(userData)
	if p == nil {
		return 0
	}
	return p.handleGetProperty(cStr(iface), cStr(prop))
}

func portalNameAcquiredTrampoline(conn, name, userData uintptr) {
	p := lookupPortal(userData)
	if p == nil {
		return
	}
	p.signalReady(nil)
}

func portalNameLostTrampoline(conn, name, userData uintptr) {
	p := lookupPortal(userData)
	if p == nil {
		return
	}
	p.signalReady(fmt.Errorf("lost D-Bus name %s", cStr(name)))
}

func portalMainInvokeTrampoline(userData uintptr) uintptr {
	portalMu.Lock()
	fn := invokeTasks[userData]
	delete(invokeTasks, userData)
	portalMu.Unlock()
	if fn != nil {
		fn()
	}
	return 0 // G_SOURCE_REMOVE
}

func portalClientSignalTrampoline(conn, sender, objPath, iface, signal, params, userData uintptr) {
	portalMu.Lock()
	fn := signalSubs[userData]
	portalMu.Unlock()
	if fn != nil {
		fn(cStr(sender), cStr(objPath), cStr(iface), cStr(signal), params)
	}
}

type PortalValue struct {
	Kind string
	U32  uint32
	Str  string
	Bool bool
	F64  float64
	RGB  [3]float64
}

func PortalU32(v uint32) PortalValue     { return PortalValue{Kind: "u", U32: v} }
func PortalStr(v string) PortalValue     { return PortalValue{Kind: "s", Str: v} }
func PortalBool(v bool) PortalValue      { return PortalValue{Kind: "b", Bool: v} }
func PortalDouble(v float64) PortalValue { return PortalValue{Kind: "d", F64: v} }
func PortalRGB(r, g, b float64) PortalValue {
	return PortalValue{Kind: "(ddd)", RGB: [3]float64{r, g, b}}
}

func (v PortalValue) toGVariant() uintptr {
	switch v.Kind {
	case "u":
		return gVariantNewUint32(v.U32)
	case "s":
		return gVariantNewString(v.Str)
	case "b":
		return gVariantNewBoolean(v.Bool)
	case "d":
		return gVariantNewDouble(v.F64)
	case "(ddd)":
		return variantTuple(
			gVariantNewDouble(v.RGB[0]),
			gVariantNewDouble(v.RGB[1]),
			gVariantNewDouble(v.RGB[2]),
		)
	default:
		return gVariantNewUint32(v.U32)
	}
}

type PortalCall struct {
	Interface string
	Method    string
	Path      string
	Args      string
	Options   map[string]string
	Timestamp time.Time
}

type mockPortalOptions struct {
	ScreenshotURI       string
	ScreenshotPNG       []byte
	ScreenshotCode      uint32
	ResponseDelay       time.Duration
	HoldRequest         bool
	ScreenshotMethodErr string
	ColorScheme         *uint32
	AccentColor         *[3]float64
	Contrast            *uint32
	InitialSettings     map[string]map[string]PortalValue
}

type activePortalReq struct {
	path   string
	regID  uint32
	closed bool
}

type mockPortal struct {
	ID      uintptr
	Addr    string
	SockDir string
	opts    mockPortalOptions

	daemonCmd *exec.Cmd

	ctx       uintptr
	loop      uintptr
	conn      uintptr
	nodeInfo  uintptr
	ownerID   uint32
	desktopID []uint32

	readyOnce sync.Once
	readyCh   chan error
	doneCh    chan struct{}

	mu         sync.Mutex
	closing    bool
	calls      []PortalCall
	settings   map[string]map[string]PortalValue
	activeReqs map[string]*activePortalReq
	nextReqSeq atomic.Uint64
	closeOnce  sync.Once
}

func startMockPortal(t *testing.T, opts mockPortalOptions) *mockPortal {
	t.Helper()
	dbusDaemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skipf("skipping D-Bus portal test: dbus-daemon not found: %v", err)
	}
	if err := initPortalGIO(); err != nil {
		t.Skipf("skipping D-Bus portal test: %v", err)
	}

	sockDir, err := os.MkdirTemp("/tmp", "h-dbus-*")
	if err != nil {
		t.Fatalf("create short dbus dir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(sockDir)
	})

	if len(opts.ScreenshotPNG) > 0 && opts.ScreenshotURI == "" {
		pngPath := filepath.Join(sockDir, "shot.png")
		if err := os.WriteFile(pngPath, opts.ScreenshotPNG, 0o644); err != nil {
			t.Fatalf("write screenshot png fixture: %v", err)
		}
		opts.ScreenshotURI = "file://" + pngPath
	}
	if opts.ResponseDelay == 0 {
		opts.ResponseDelay = 5 * time.Millisecond
	}

	confPath := filepath.Join(sockDir, "bus.conf")
	sockPath := filepath.Join(sockDir, "bus")
	conf := fmt.Sprintf(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=%s</listen>
  <policy context="default">
    <allow send_destination="*"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`, sockPath)
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatalf("write bus.conf: %v", err)
	}

	cmd := exec.Command(dbusDaemon, "--config-file="+confPath, "--nofork", "--print-address")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("dbus-daemon stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dbus-daemon: %v", err)
	}

	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			addrCh <- strings.TrimSpace(sc.Text())
			return
		}
		if err := sc.Err(); err != nil {
			errCh <- err
			return
		}
		errCh <- fmt.Errorf("dbus-daemon exited without printing address")
	}()

	var busAddr string
	select {
	case busAddr = <-addrCh:
	case err := <-errCh:
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("dbus-daemon startup failed: %v", err)
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("timed out waiting for dbus-daemon --print-address")
	}

	settings := make(map[string]map[string]PortalValue)
	for ns, kv := range opts.InitialSettings {
		cp := make(map[string]PortalValue, len(kv))
		for k, v := range kv {
			cp[k] = v
		}
		settings[ns] = cp
	}
	ensureNS := func(ns string) map[string]PortalValue {
		if settings[ns] == nil {
			settings[ns] = map[string]PortalValue{}
		}
		return settings[ns]
	}
	if opts.ColorScheme != nil {
		ensureNS(portalAppearanceNS)[portalColorSchemeKey] = PortalU32(*opts.ColorScheme)
	}
	if opts.AccentColor != nil {
		c := *opts.AccentColor
		ensureNS(portalAppearanceNS)[portalAccentColorKey] = PortalRGB(c[0], c[1], c[2])
	}
	if opts.Contrast != nil {
		ensureNS(portalAppearanceNS)[portalContrastKey] = PortalU32(*opts.Contrast)
	}

	p := &mockPortal{
		ID:         allocPortalID(),
		Addr:       busAddr,
		SockDir:    sockDir,
		opts:       opts,
		daemonCmd:  cmd,
		readyCh:    make(chan error, 1),
		doneCh:     make(chan struct{}),
		settings:   settings,
		activeReqs: map[string]*activePortalReq{},
	}

	portalMu.Lock()
	portalRegistry[p.ID] = p
	portalMu.Unlock()

	go p.runLoop()

	select {
	case err := <-p.readyCh:
		if err != nil {
			p.Close()
			t.Fatalf("mock portal startup failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		p.Close()
		t.Fatalf("timed out waiting for mock portal D-Bus name ownership")
	}

	t.Cleanup(p.Close)
	return p
}

func (p *mockPortal) signalReady(err error) {
	p.readyOnce.Do(func() {
		p.readyCh <- err
	})
}

func (p *mockPortal) runLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(p.doneCh)

	ctx := gMainContextNew()
	gMainContextPushThreadDef(ctx)
	defer func() {
		gMainContextPopThreadDef(ctx)
		gMainContextUnref(ctx)
		portalMu.Lock()
		delete(portalRegistry, p.ID)
		portalMu.Unlock()
	}()

	var gerr uintptr
	conn := gDBusConnNewForAddressSync(p.Addr, gDBusConnFlagsAuthClientMessageBus, 0, 0, &gerr)
	if conn == 0 {
		p.signalReady(fmt.Errorf("g_dbus_connection_new_for_address_sync: %s", consumeGError(gerr)))
		return
	}
	gDBusConnSetExitOnClose(conn, false)

	nodeInfo := gDBusNodeInfoNewForXML(portalIntrospectionXML, &gerr)
	if nodeInfo == 0 {
		_ = gDBusConnCloseSync(conn, 0, nil)
		p.signalReady(fmt.Errorf("g_dbus_node_info_new_for_xml: %s", consumeGError(gerr)))
		return
	}

	var regIDs []uint32
	for _, ifaceName := range []string{portalScreenshotIf, portalSettingsIf} {
		ifaceInfo := gDBusNodeInfoLookupIface(nodeInfo, ifaceName)
		regID := gDBusConnRegisterObject(conn, portalObjectPath, ifaceInfo, unsafe.Pointer(&portalVtable[0]), p.ID, 0, &gerr)
		if regID == 0 {
			for _, id := range regIDs {
				gDBusConnUnregisterObject(conn, id)
			}
			gDBusNodeInfoUnref(nodeInfo)
			_ = gDBusConnCloseSync(conn, 0, nil)
			p.signalReady(fmt.Errorf("register %s: %s", ifaceName, consumeGError(gerr)))
			return
		}
		regIDs = append(regIDs, regID)
	}

	loop := gMainLoopNew(ctx, false)
	p.mu.Lock()
	p.ctx = ctx
	p.loop = loop
	p.conn = conn
	p.nodeInfo = nodeInfo
	p.desktopID = regIDs
	p.ownerID = gBusOwnNameOnConn(conn, portalBusName, gBusNameOwnerFlagsDoNotQueue, portalNameAcquiredCB, portalNameLostCB, p.ID, 0)
	p.mu.Unlock()

	gMainLoopRun(loop)

	p.mu.Lock()
	p.closing = true
	for _, req := range p.activeReqs {
		if req.regID != 0 {
			gDBusConnUnregisterObject(conn, req.regID)
			req.regID = 0
		}
	}
	for _, id := range p.desktopID {
		gDBusConnUnregisterObject(conn, id)
	}
	p.desktopID = nil
	if p.ownerID != 0 {
		gBusUnownName(p.ownerID)
		p.ownerID = 0
	}
	p.mu.Unlock()

	_ = gDBusConnCloseSync(conn, 0, nil)
	gDBusNodeInfoUnref(nodeInfo)
	gMainLoopUnref(loop)
}

func (p *mockPortal) invokeOnLoop(fn func()) {
	p.mu.Lock()
	if p.closing || p.ctx == 0 {
		p.mu.Unlock()
		return
	}
	ctx := p.ctx
	p.mu.Unlock()

	id := allocPortalID()
	portalMu.Lock()
	invokeTasks[id] = fn
	portalMu.Unlock()
	gMainContextInvoke(ctx, portalMainInvokeCB, id)
}

func (p *mockPortal) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		loop := p.loop
		ctx := p.ctx
		p.closing = true
		p.mu.Unlock()

		if loop != 0 && ctx != 0 {
			id := allocPortalID()
			portalMu.Lock()
			invokeTasks[id] = func() {
				gMainLoopQuit(loop)
			}
			portalMu.Unlock()
			gMainContextInvoke(ctx, portalMainInvokeCB, id)
			select {
			case <-p.doneCh:
			case <-time.After(3 * time.Second):
			}
		}

		if p.daemonCmd != nil && p.daemonCmd.Process != nil {
			_ = p.daemonCmd.Process.Signal(os.Interrupt)
			waitCh := make(chan struct{})
			go func() {
				_ = p.daemonCmd.Wait()
				close(waitCh)
			}()
			select {
			case <-waitCh:
			case <-time.After(1 * time.Second):
				_ = p.daemonCmd.Process.Kill()
				<-waitCh
			}
		}
	})
}

func (p *mockPortal) Calls() []PortalCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PortalCall, len(p.calls))
	copy(out, p.calls)
	return out
}

func (p *mockPortal) SetSetting(ns, key string, val PortalValue) {
	p.mu.Lock()
	if p.settings[ns] == nil {
		p.settings[ns] = map[string]PortalValue{}
	}
	p.settings[ns][key] = val
	p.mu.Unlock()
}

func (p *mockPortal) EmitSettingChanged(ns, key string, val PortalValue) {
	p.SetSetting(ns, key, val)
	p.invokeOnLoop(func() {
		p.mu.Lock()
		conn := p.conn
		closing := p.closing
		p.mu.Unlock()
		if closing || conn == 0 {
			return
		}
		params := variantTuple(
			gVariantNewString(ns),
			gVariantNewString(key),
			gVariantNewVariant(val.toGVariant()),
		)
		var gerr uintptr
		if !gDBusConnEmitSignal(conn, 0, portalObjectPath, portalSettingsIf, "SettingChanged", params, &gerr) {
			_ = consumeGError(gerr)
		}
	})
}

func (p *mockPortal) SetColorScheme(scheme uint32) {
	p.EmitSettingChanged(portalAppearanceNS, portalColorSchemeKey, PortalU32(scheme))
}

func (p *mockPortal) SetAccentColor(r, g, b float64) {
	p.EmitSettingChanged(portalAppearanceNS, portalAccentColorKey, PortalRGB(r, g, b))
}

func (p *mockPortal) recordCall(call PortalCall) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
}

func (p *mockPortal) handleGetProperty(iface, prop string) uintptr {
	switch iface {
	case portalScreenshotIf:
		switch prop {
		case "version":
			return gVariantNewUint32(3)
		case "AvailableTargets":
			return gVariantNewUint32(7)
		}
	case portalSettingsIf:
		switch prop {
		case "version":
			return gVariantNewUint32(2)
		}
	}
	return 0
}

func (p *mockPortal) handleMethodCall(sender, objPath, iface, method string, params, invocation uintptr) {
	argsStr := ""
	if params != 0 {
		printed := gVariantPrint(params, false)
		argsStr = cStr(printed)
		if printed != 0 {
			gFree(printed)
		}
	}
	optsMap := map[string]string{}

	switch iface {
	case portalScreenshotIf:
		if method == "Screenshot" || method == "PickColor" {
			optsChild := gVariantGetChildValue(params, 1)
			optsMap = extractVariantDictSummary(optsChild)
			gVariantUnref(optsChild)

			p.recordCall(PortalCall{
				Interface: iface,
				Method:    method,
				Path:      objPath,
				Args:      argsStr,
				Options:   optsMap,
				Timestamp: time.Now(),
			})

			if p.opts.ScreenshotMethodErr != "" {
				gDBusMethodInvReturnError(invocation, p.opts.ScreenshotMethodErr, "mock screenshot method error")
				return
			}

			token := optsMap["handle_token"]
			if token == "" {
				token = fmt.Sprintf("h%d", p.nextReqSeq.Add(1))
			}
			reqPath := "/org/freedesktop/portal/desktop/request/" + sanitizeSender(sender) + "/" + token

			p.mu.Lock()
			reqIface := gDBusNodeInfoLookupIface(p.nodeInfo, portalRequestIf)
			var gerr uintptr
			regID := gDBusConnRegisterObject(p.conn, reqPath, reqIface, unsafe.Pointer(&portalVtable[0]), p.ID, 0, &gerr)
			if regID == 0 {
				p.mu.Unlock()
				gDBusMethodInvReturnError(invocation, "org.freedesktop.portal.Error.Failed", consumeGError(gerr))
				return
			}
			req := &activePortalReq{path: reqPath, regID: regID}
			p.activeReqs[reqPath] = req
			hold := p.opts.HoldRequest
			delay := p.opts.ResponseDelay
			code := p.opts.ScreenshotCode
			uri := p.opts.ScreenshotURI
			p.mu.Unlock()

			gDBusMethodInvReturnValue(invocation, variantTuple(gVariantNewObjectPath(reqPath)))

			if !hold {
				time.AfterFunc(delay, func() {
					p.invokeOnLoop(func() {
						p.mu.Lock()
						if p.closing || req.closed || req.regID == 0 {
							p.mu.Unlock()
							return
						}
						gDBusConnUnregisterObject(p.conn, req.regID)
						req.regID = 0
						delete(p.activeReqs, reqPath)
						conn := p.conn
						p.mu.Unlock()

						var results uintptr
						if code == 0 && uri != "" {
							entry := gVariantNewDictEntry(
								gVariantNewString("uri"),
								gVariantNewVariant(gVariantNewString(uri)),
							)
							results = gVariantNewArray(varTypeSV, unsafe.Pointer(&entry), 1)
						} else {
							results = variantEmptyDictSV()
						}
						sigParams := variantTuple(gVariantNewUint32(code), results)
						var sgerr uintptr
						if !gDBusConnEmitSignal(conn, 0, reqPath, portalRequestIf, "Response", sigParams, &sgerr) {
							_ = consumeGError(sgerr)
						}
					})
				})
			}
			return
		}

	case portalRequestIf:
		if method == "Close" {
			p.recordCall(PortalCall{
				Interface: iface,
				Method:    method,
				Path:      objPath,
				Args:      argsStr,
				Timestamp: time.Now(),
			})
			p.mu.Lock()
			if req, ok := p.activeReqs[objPath]; ok {
				req.closed = true
				if req.regID != 0 {
					gDBusConnUnregisterObject(p.conn, req.regID)
					req.regID = 0
				}
				delete(p.activeReqs, objPath)
			}
			p.mu.Unlock()
			gDBusMethodInvReturnValue(invocation, 0)
			return
		}

	case portalSettingsIf:
		p.recordCall(PortalCall{
			Interface: iface,
			Method:    method,
			Path:      objPath,
			Args:      argsStr,
			Timestamp: time.Now(),
		})
		switch method {
		case "Read", "ReadOne":
			nsVar := gVariantGetChildValue(params, 0)
			keyVar := gVariantGetChildValue(params, 1)
			ns := cStr(gVariantGetString(nsVar, 0))
			key := cStr(gVariantGetString(keyVar, 0))
			gVariantUnref(nsVar)
			gVariantUnref(keyVar)

			p.mu.Lock()
			val, ok := p.settings[ns][key]
			p.mu.Unlock()
			if !ok {
				gDBusMethodInvReturnError(invocation, portalNotFoundErr, "Requested setting not found")
				return
			}
			inner := val.toGVariant()
			if method == "Read" {
				// Deprecated Read() returns double-wrapped variant (<<v>>,).
				gDBusMethodInvReturnValue(invocation, variantTuple(gVariantNewVariant(gVariantNewVariant(inner))))
			} else {
				// ReadOne() returns single-wrapped variant (<v>,).
				gDBusMethodInvReturnValue(invocation, variantTuple(gVariantNewVariant(inner)))
			}
			return

		case "ReadAll":
			nsArr := gVariantGetChildValue(params, 0)
			n := int(gVariantNChildren(nsArr))
			patterns := make([]string, 0, n)
			for i := 0; i < n; i++ {
				ch := gVariantGetChildValue(nsArr, uintptr(i))
				patterns = append(patterns, cStr(gVariantGetString(ch, 0)))
				gVariantUnref(ch)
			}
			gVariantUnref(nsArr)

			p.mu.Lock()
			outerEntries := make([]uintptr, 0, len(p.settings))
			for ns, kv := range p.settings {
				if !matchPortalNamespaces(ns, patterns) || len(kv) == 0 {
					continue
				}
				innerEntries := make([]uintptr, 0, len(kv))
				for k, v := range kv {
					innerEntries = append(innerEntries, gVariantNewDictEntry(
						gVariantNewString(k),
						gVariantNewVariant(v.toGVariant()),
					))
				}
				innerDict := gVariantNewArray(varTypeSV, unsafe.Pointer(&innerEntries[0]), uintptr(len(innerEntries)))
				outerEntries = append(outerEntries, gVariantNewDictEntry(gVariantNewString(ns), innerDict))
			}
			p.mu.Unlock()

			var outerDict uintptr
			if len(outerEntries) == 0 {
				outerDict = gVariantNewArray(varTypeSAASV, nil, 0)
			} else {
				outerDict = gVariantNewArray(varTypeSAASV, unsafe.Pointer(&outerEntries[0]), uintptr(len(outerEntries)))
			}
			gDBusMethodInvReturnValue(invocation, variantTuple(outerDict))
			return
		}
	}

	gDBusMethodInvReturnError(invocation, "org.freedesktop.DBus.Error.UnknownMethod", "unknown method "+iface+"."+method)
}

func sanitizeSender(sender string) string {
	s := strings.TrimPrefix(sender, ":")
	s = strings.ReplaceAll(s, ".", "_")
	if s == "" {
		return "unknown"
	}
	return s
}

func matchPortalNamespaces(ns string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pat := range patterns {
		if pat == "" || pat == "*" || pat == ns {
			return true
		}
		if strings.HasSuffix(pat, ".*") {
			prefix := strings.TrimSuffix(pat, ".*")
			if ns == prefix || strings.HasPrefix(ns, prefix+".") {
				return true
			}
		}
	}
	return false
}

func extractVariantDictSummary(dict uintptr) map[string]string {
	out := map[string]string{}
	if dict == 0 {
		return out
	}
	n := int(gVariantNChildren(dict))
	for i := 0; i < n; i++ {
		entry := gVariantGetChildValue(dict, uintptr(i))
		kVar := gVariantGetChildValue(entry, 0)
		vWrap := gVariantGetChildValue(entry, 1)
		v := gVariantGetVariant(vWrap)
		key := cStr(gVariantGetString(kVar, 0))
		typ := cStr(gVariantGetTypeString(v))
		switch typ {
		case "s":
			out[key] = cStr(gVariantGetString(v, 0))
		case "b":
			if gVariantGetBoolean(v) {
				out[key] = "true"
			} else {
				out[key] = "false"
			}
		case "u":
			out[key] = fmt.Sprintf("%d", gVariantGetUint32(v))
		default:
			printed := gVariantPrint(v, false)
			out[key] = cStr(printed)
			if printed != 0 {
				gFree(printed)
			}
		}
		gVariantUnref(v)
		gVariantUnref(vWrap)
		gVariantUnref(kVar)
		gVariantUnref(entry)
	}
	return out
}

func TestHarness_MockPortalLinux(t *testing.T) {
	scheme := uint32(1)
	accent := [3]float64{0.2, 0.5, 0.9}
	portal := startMockPortal(t, mockPortalOptions{
		ScreenshotPNG: makePNG(t, 80, 60),
		ColorScheme:   &scheme,
		AccentColor:   &accent,
	})

	if hostAddr := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); hostAddr != "" && hostAddr == portal.Addr {
		t.Fatalf("mock portal bus address %q collided with host bus", portal.Addr)
	}

	// Open an isolated client connection directly by address (never g_bus_get_sync).
	var gerr uintptr
	cliConn := gDBusConnNewForAddressSync(portal.Addr, gDBusConnFlagsAuthClientMessageBus, 0, 0, &gerr)
	if cliConn == 0 {
		t.Fatalf("client connect to %s: %s", portal.Addr, consumeGError(gerr))
	}
	gDBusConnSetExitOnClose(cliConn, false)
	defer func() {
		_ = gDBusConnCloseSync(cliConn, 0, nil)
	}()

	// 1. Settings.Read -> double-wrapped variant (<<uint32 1>>,).
	readRes := gDBusConnCallSync(cliConn, portalBusName, portalObjectPath, portalSettingsIf, "Read",
		variantTuple(gVariantNewString(portalAppearanceNS), gVariantNewString(portalColorSchemeKey)),
		0, 0, 3000, 0, &gerr)
	if readRes == 0 {
		t.Fatalf("Settings.Read failed: %s", consumeGError(gerr))
	}
	outerV := gVariantGetChildValue(readRes, 0)
	midV := gVariantGetVariant(outerV)
	if typ := cStr(gVariantGetTypeString(midV)); typ != "v" {
		t.Fatalf("Settings.Read inner type = %q, want double-variant \"v\"", typ)
	}
	innerV := gVariantGetVariant(midV)
	if got := gVariantGetUint32(innerV); got != 1 {
		t.Fatalf("Settings.Read color-scheme = %d, want 1", got)
	}
	gVariantUnref(innerV)
	gVariantUnref(midV)
	gVariantUnref(outerV)
	gVariantUnref(readRes)

	// 2. Settings.ReadOne -> single-wrapped variant (<uint32 1>,).
	readOneRes := gDBusConnCallSync(cliConn, portalBusName, portalObjectPath, portalSettingsIf, "ReadOne",
		variantTuple(gVariantNewString(portalAppearanceNS), gVariantNewString(portalColorSchemeKey)),
		0, 0, 3000, 0, &gerr)
	if readOneRes == 0 {
		t.Fatalf("Settings.ReadOne failed: %s", consumeGError(gerr))
	}
	oneOuter := gVariantGetChildValue(readOneRes, 0)
	oneInner := gVariantGetVariant(oneOuter)
	if typ := cStr(gVariantGetTypeString(oneInner)); typ != "u" {
		t.Fatalf("Settings.ReadOne inner type = %q, want \"u\"", typ)
	}
	if got := gVariantGetUint32(oneInner); got != 1 {
		t.Fatalf("Settings.ReadOne color-scheme = %d, want 1", got)
	}
	gVariantUnref(oneInner)
	gVariantUnref(oneOuter)
	gVariantUnref(readOneRes)

	// 3. Settings.ReadAll with globbing ["org.freedesktop.*"].
	glob := gVariantNewString("org.freedesktop.*")
	globArr := gVariantNewArray(0, unsafe.Pointer(&glob), 1)
	readAllRes := gDBusConnCallSync(cliConn, portalBusName, portalObjectPath, portalSettingsIf, "ReadAll",
		variantTuple(globArr), 0, 0, 3000, 0, &gerr)
	if readAllRes == 0 {
		t.Fatalf("Settings.ReadAll failed: %s", consumeGError(gerr))
	}
	allDict := gVariantGetChildValue(readAllRes, 0)
	nsDict := gVariantLookupValue(allDict, portalAppearanceNS, varTypeSASV)
	if nsDict == 0 {
		t.Fatalf("Settings.ReadAll missing %s", portalAppearanceNS)
	}
	accentV := gVariantLookupValue(nsDict, portalAccentColorKey, 0)
	if accentV == 0 {
		t.Fatalf("Settings.ReadAll missing %s", portalAccentColorKey)
	}
	c0 := gVariantGetChildValue(accentV, 0)
	if r := gVariantGetDouble(c0); r < 0.19 || r > 0.21 {
		t.Fatalf("accent-color R = %v, want 0.2", r)
	}
	gVariantUnref(c0)
	gVariantUnref(accentV)
	gVariantUnref(nsDict)
	gVariantUnref(allDict)
	gVariantUnref(readAllRes)

	// 3b. Settings.ReadAll with empty []string{}, ["org.freedesktop.appearance.*"], and ["*"].
	for _, tc := range []struct {
		name string
		arr  func() uintptr
	}{
		{
			name: "empty",
			arr:  func() uintptr { return gVariantNewArray(varTypeS, nil, 0) },
		},
		{
			name: "appearance_glob",
			arr: func() uintptr {
				s := gVariantNewString("org.freedesktop.appearance.*")
				return gVariantNewArray(0, unsafe.Pointer(&s), 1)
			},
		},
		{
			name: "wildcard_star",
			arr: func() uintptr {
				s := gVariantNewString("*")
				return gVariantNewArray(0, unsafe.Pointer(&s), 1)
			},
		},
	} {
		res := gDBusConnCallSync(cliConn, portalBusName, portalObjectPath, portalSettingsIf, "ReadAll",
			variantTuple(tc.arr()), 0, 0, 3000, 0, &gerr)
		if res == 0 {
			t.Fatalf("Settings.ReadAll(%s) failed: %s", tc.name, consumeGError(gerr))
		}
		d := gVariantGetChildValue(res, 0)
		ns := gVariantLookupValue(d, portalAppearanceNS, varTypeSASV)
		if ns == 0 {
			t.Fatalf("Settings.ReadAll(%s) missing %s", tc.name, portalAppearanceNS)
		}
		gVariantUnref(ns)
		gVariantUnref(d)
		gVariantUnref(res)
	}

	// 4. Spin a lightweight GLib main loop on a client thread to receive SettingChanged + Response signals.
	sigCh := make(chan string, 8)
	subID := allocPortalID()
	portalMu.Lock()
	signalSubs[subID] = func(_, path, iface, signal string, params uintptr) {
		printed := gVariantPrint(params, false)
		s := cStr(printed)
		if printed != 0 {
			gFree(printed)
		}
		sigCh <- fmt.Sprintf("%s|%s.%s|%s", path, iface, signal, s)
	}
	portalMu.Unlock()
	defer func() {
		portalMu.Lock()
		delete(signalSubs, subID)
		portalMu.Unlock()
	}()

	cliCtx := gMainContextNew()
	cliLoop := gMainLoopNew(cliCtx, false)
	cliDone := make(chan struct{})
	cliSubReady := make(chan uint32, 2)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(cliDone)
		gMainContextPushThreadDef(cliCtx)
		defer gMainContextPopThreadDef(cliCtx)
		s1 := gDBusConnSignalSubscribe(cliConn, nil, cStrPtr(portalSettingsIf), cStrPtr("SettingChanged"), cStrPtr(portalObjectPath), nil, 0, portalClientSignalCB, subID, 0)
		s2 := gDBusConnSignalSubscribe(cliConn, nil, cStrPtr(portalRequestIf), cStrPtr("Response"), nil, nil, 0, portalClientSignalCB, subID, 0)
		cliSubReady <- s1
		cliSubReady <- s2
		gMainLoopRun(cliLoop)
		gDBusConnSignalUnsubscribe(cliConn, s1)
		gDBusConnSignalUnsubscribe(cliConn, s2)
	}()
	<-cliSubReady
	<-cliSubReady
	if pingRes := gDBusConnCallSync(cliConn, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.Peer", "Ping", 0, 0, 0, 3000, 0, &gerr); pingRes == 0 {
		t.Fatalf("cliConn Peer.Ping AddMatch barrier failed: %s", consumeGError(gerr))
	} else {
		gVariantUnref(pingRes)
	}
	defer func() {
		id := allocPortalID()
		portalMu.Lock()
		invokeTasks[id] = func() { gMainLoopQuit(cliLoop) }
		portalMu.Unlock()
		gMainContextInvoke(cliCtx, portalMainInvokeCB, id)
		<-cliDone
		gMainLoopUnref(cliLoop)
		gMainContextUnref(cliCtx)
	}()

	portal.SetColorScheme(2)
	select {
	case sig := <-sigCh:
		if !strings.Contains(sig, "SettingChanged") || !strings.Contains(sig, "color-scheme") || !strings.Contains(sig, "uint32 2") {
			t.Fatalf("unexpected SettingChanged signal: %q", sig)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for SettingChanged signal")
	}

	// 5. Screenshot -> Request::Response signal.
	tokEntry := gVariantNewDictEntry(
		gVariantNewString("handle_token"),
		gVariantNewVariant(gVariantNewString("tok1")),
	)
	intEntry := gVariantNewDictEntry(
		gVariantNewString("interactive"),
		gVariantNewVariant(gVariantNewBoolean(true)),
	)
	optsEntries := [2]uintptr{tokEntry, intEntry}
	optsArr := gVariantNewArray(varTypeSV, unsafe.Pointer(&optsEntries[0]), 2)
	shotRes := gDBusConnCallSync(cliConn, portalBusName, portalObjectPath, portalScreenshotIf, "Screenshot",
		variantTuple(gVariantNewString(""), optsArr), 0, 0, 3000, 0, &gerr)
	if shotRes == 0 {
		t.Fatalf("Screenshot failed: %s", consumeGError(gerr))
	}
	handleVar := gVariantGetChildValue(shotRes, 0)
	handlePath := cStr(gVariantGetString(handleVar, 0))
	gVariantUnref(handleVar)
	gVariantUnref(shotRes)
	if !strings.HasSuffix(handlePath, "/tok1") {
		t.Fatalf("unexpected Screenshot handle %q", handlePath)
	}

	select {
	case sig := <-sigCh:
		if !strings.Contains(sig, handlePath) || !strings.Contains(sig, "Response") || !strings.Contains(sig, "file://") {
			t.Fatalf("unexpected Request::Response signal: %q", sig)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for Request::Response signal")
	}

	// 6. Screenshot with HoldRequest=true + Request.Close() -> must NOT emit Response.
	heldPortal := startMockPortal(t, mockPortalOptions{
		ScreenshotPNG: makePNG(t, 40, 40),
		HoldRequest:   true,
	})
	heldConn := gDBusConnNewForAddressSync(heldPortal.Addr, gDBusConnFlagsAuthClientMessageBus, 0, 0, &gerr)
	if heldConn == 0 {
		t.Fatalf("held client connect: %s", consumeGError(gerr))
	}
	gDBusConnSetExitOnClose(heldConn, false)
	defer func() { _ = gDBusConnCloseSync(heldConn, 0, nil) }()

	heldRespCh := make(chan string, 1)
	heldSubID := allocPortalID()
	portalMu.Lock()
	signalSubs[heldSubID] = func(_, path, iface, signal string, _ uintptr) {
		heldRespCh <- path + "|" + iface + "." + signal
	}
	portalMu.Unlock()
	defer func() {
		portalMu.Lock()
		delete(signalSubs, heldSubID)
		portalMu.Unlock()
	}()
	heldCtx := gMainContextNew()
	heldLoop := gMainLoopNew(heldCtx, false)
	heldDone := make(chan struct{})
	heldSubReady := make(chan struct{}, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(heldDone)
		gMainContextPushThreadDef(heldCtx)
		defer gMainContextPopThreadDef(heldCtx)
		hSub := gDBusConnSignalSubscribe(heldConn, nil, cStrPtr(portalRequestIf), cStrPtr("Response"), nil, nil, 0, portalClientSignalCB, heldSubID, 0)
		heldSubReady <- struct{}{}
		gMainLoopRun(heldLoop)
		gDBusConnSignalUnsubscribe(heldConn, hSub)
	}()
	<-heldSubReady
	if pingRes := gDBusConnCallSync(heldConn, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.Peer", "Ping", 0, 0, 0, 3000, 0, &gerr); pingRes == 0 {
		t.Fatalf("heldConn Peer.Ping AddMatch barrier failed: %s", consumeGError(gerr))
	} else {
		gVariantUnref(pingRes)
	}
	defer func() {
		id := allocPortalID()
		portalMu.Lock()
		invokeTasks[id] = func() { gMainLoopQuit(heldLoop) }
		portalMu.Unlock()
		gMainContextInvoke(heldCtx, portalMainInvokeCB, id)
		<-heldDone
		gMainLoopUnref(heldLoop)
		gMainContextUnref(heldCtx)
	}()

	heldTok := gVariantNewDictEntry(
		gVariantNewString("handle_token"),
		gVariantNewVariant(gVariantNewString("tok_close")),
	)
	heldOpts := gVariantNewArray(varTypeSV, unsafe.Pointer(&heldTok), 1)
	heldShot := gDBusConnCallSync(heldConn, portalBusName, portalObjectPath, portalScreenshotIf, "Screenshot",
		variantTuple(gVariantNewString(""), heldOpts), 0, 0, 3000, 0, &gerr)
	if heldShot == 0 {
		t.Fatalf("held Screenshot failed: %s", consumeGError(gerr))
	}
	hVar := gVariantGetChildValue(heldShot, 0)
	hPath := cStr(gVariantGetString(hVar, 0))
	gVariantUnref(hVar)
	gVariantUnref(heldShot)

	closeRes := gDBusConnCallSync(heldConn, portalBusName, hPath, portalRequestIf, "Close", 0, 0, 0, 3000, 0, &gerr)
	if closeRes == 0 {
		t.Fatalf("Request.Close failed: %s", consumeGError(gerr))
	}
	gVariantUnref(closeRes)

	select {
	case unexpected := <-heldRespCh:
		t.Fatalf("Request.Close() should suppress Response, got %q", unexpected)
	case <-time.After(100 * time.Millisecond):
	}
}

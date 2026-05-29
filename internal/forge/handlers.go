package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/StephenSHorton/wc3-forge/internal/bridge"
	"github.com/StephenSHorton/wc3-forge/internal/formats/doodadsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3i"
)

// bridgeRef holds the bridge pointer captured by RegisterAll so handlers and
// the Wails App can read live port/token after Start() populates them. Set
// once at startup, never mutated after.
//
// Goes through a single accessor (BridgeIdentity) so callers don't have to
// branch on nil — important for the test path which calls handlers without
// ever spinning up a real bridge.
var bridgeRef *bridge.Bridge

// BridgeIdentity returns the running bridge's pid, port, and SHORTENED auth
// token (first 8 hex chars). Pid falls back to os.Getpid() when no bridge is
// registered (test path); port/token return zero values so JSON consumers
// see "no bridge" cleanly instead of garbage.
//
// The shortened token is intentional: the full secret never enters UI that
// might be screenshotted or screen-shared. Agents needing to authenticate
// already have the full token from the lockfile.
func BridgeIdentity() (pid int, port int, tokenShort string) {
	pid = os.Getpid()
	if bridgeRef == nil {
		return pid, 0, ""
	}
	return pid, bridgeRef.Port(), bridgeRef.TokenShort()
}

// RegisterAll wires every wc3-forge MCP handler into the bridge. Called once
// from main before Bridge.Start.
//
// Each handler is wrapped with `instrumented` so the bridge-call observability
// bus fires for every dispatch (Agent Console subscribers see method, params,
// result, duration, and any error). Wrapping happens here so individual
// handler implementations stay focused on their domain logic.
//
// Captures the bridge pointer into bridgeRef so handlers + App can read
// port/token AFTER Start() has populated them.
func RegisterAll(b *bridge.Bridge) {
	bridgeRef = b
	reg := func(method string, h bridge.Handler) {
		b.Register(method, instrumented(method, h))
	}
	reg("bridge.ping", handlePing)
	reg("map.open", handleMapOpen)
	reg("map.close", handleMapClose)
	reg("map.status", handleMapStatus)
	reg("map.info_get", handleMapInfoGet)
	reg("map.info_set", handleMapInfoSet)
	reg("units.list", handleUnitsList)
	reg("units.get", handleUnitsGet)
	reg("units.move", handleUnitsMove)
	reg("units.rotate", handleUnitsRotate)
	reg("units.scale", handleUnitsScale)
	reg("doodads.list", handleDoodadsList)
	reg("doodads.get", handleDoodadsGet)
	reg("doodads.move", handleDoodadsMove)
	reg("doodads.rotate", handleDoodadsRotate)
	reg("doodads.scale", handleDoodadsScale)
	// Placed-entity create/delete (units + doodads). Undo-aware; emit
	// entity-changed. See handlers_entities.go + entities_mutate.go.
	reg("units.create", handleUnitsCreate)
	reg("units.delete", handleUnitsDelete)
	reg("doodads.create", handleDoodadsCreate)
	reg("doodads.delete", handleDoodadsDelete)
	reg("map.save", handleMapSave)
	reg("selection.get", handleSelectionGet)
	reg("selection.set", handleSelectionSet)
	reg("selection.clear", handleSelectionClear)
	reg("view.set_mode", handleViewSetMode)
	reg("view.set_doodad_category_visible", handleViewSetDoodadCategoryVisible)
	reg("camera.set_view", handleCameraSetView)
	// window.set_title — connected agent labels its wc3-forge window so the
	// user can tell parallel instances apart in the taskbar/alt-tab list.
	// Free-form short string; the App layer composes it into the OS title as
	// "[*] <map> — <label> — PID <n>".
	reg("window.set_title", handleWindowSetTitle)
	// Undo/redo + transactional grouping. AI clients can drive these the same
	// way the UI does (Ctrl+Z is just a hotkey wrapper around history.undo);
	// agents that batch multi-step edits should bracket them with
	// history.begin_group / history.end_group so the user sees ONE undo step.
	reg("history.undo", handleHistoryUndo)
	reg("history.redo", handleHistoryRedo)
	reg("history.list", handleHistoryList)
	reg("history.begin_group", handleHistoryBeginGroup)
	reg("history.end_group", handleHistoryEndGroup)
	// _ui.send_command — escape hatch for test/verification drivers. Routes
	// a raw test-command string through Session.EmitUICommand → App →
	// wc3-forge:test-command Wails event → existing App.svelte dispatch.
	// Prefixed with underscore to flag it as a non-stable surface; agents
	// should use the specific handlers (view.set_mode etc.) where available.
	reg("_ui.send_command", handleUISendCommand)
	// Object Editor — kind-agnostic. The "objects.*" namespace is distinct
	// from "units.*"/"doodads.*", which are reserved for *placed* entities
	// (war3mapUnits.doo / war3map.doo). Phase 2b wires all seven kinds:
	// units, items, abilities, buffs, destructables, doodads, upgrades.
	//
	// Each call stamps out six routes:
	//   objects.<kind>.list / .get / .fields_meta
	//   objects.<kind>.set_field / .create_custom / .delete_custom
	registerObjectKind(reg, UnitsConfig())
	registerObjectKind(reg, ItemsConfig())
	registerObjectKind(reg, AbilitiesConfig())
	registerObjectKind(reg, BuffsConfig())
	registerObjectKind(reg, DestructablesConfig())
	registerObjectKind(reg, DoodadsConfig())
	registerObjectKind(reg, UpgradesConfig())
	// Cross-kind conversion (doodad↔destructable today; extensible to other
	// pairs via Session.ConvertObject's whitelist). Single MCP route since the
	// shape is symmetric — caller names src/dst kinds in params.
	reg("objects.convert", handleObjectsConvert)
	// Trigger Editor (Phase 1a — read-only). Three handlers:
	// triggers.tree (full hierarchy in one shot), triggers.get (full ECA +
	// custom_text for one node), triggers.functions_meta (TriggerData.txt
	// vocabulary for client-side label templating).
	registerTriggerHandlers(reg)
}

// handleUISendCommand forwards a raw test-driver command string. Used by
// verification scripts that need to drive UI state not yet covered by a
// dedicated handler (e.g. bridge_console.open). Free-form on purpose; the
// JS-side dispatcher is the source of truth for valid commands.
type uiSendCommandParams struct {
	Cmd string `json:"cmd"`
}

func handleUISendCommand(params json.RawMessage) (any, error) {
	var p uiSendCommandParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Cmd == "" {
		return nil, errors.New("cmd is required")
	}
	Current.EmitUICommand(p.Cmd)
	return map[string]any{"ok": true, "cmd": p.Cmd}, nil
}

// instrumented wraps a bridge handler so every dispatch fires a
// BridgeCallEvent through Session.notifyBridgeCall. Passive — the wrapper
// must never change the handler's behavior (return value, error, side
// effects). Truncation length matches the BridgeConsole row width budget
// so the JS side doesn't need to re-truncate.
const bridgeCallSummaryCap = 120

func instrumented(method string, h bridge.Handler) bridge.Handler {
	return func(params json.RawMessage) (any, error) {
		start := time.Now()
		result, err := h(params)
		dur := time.Since(start)
		ev := BridgeCallEvent{
			Timestamp:      start.UTC(),
			Method:         method,
			ParamsSummary:  summarizeJSON(params),
			DurationMicros: dur.Microseconds(),
		}
		if err != nil {
			ev.Error = err.Error()
		} else {
			ev.Result = summarizeResult(result)
		}
		Current.NotifyBridgeCall(ev)
		return result, err
	}
}

// summarizeJSON shrinks a raw params payload to a one-line preview. Returns
// "" for empty/null/whitespace-only inputs so the Agent Console can render
// an empty params column instead of `null`.
func summarizeJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "{}" {
		return ""
	}
	return truncate(s, bridgeCallSummaryCap)
}

// summarizeResult renders a handler return value for the console. Voids and
// {"ok": true}-shaped acks collapse to "ok" so the column stays scannable;
// everything else is JSON-marshaled + truncated.
func summarizeResult(r any) string {
	if r == nil {
		return "ok"
	}
	b, err := json.Marshal(r)
	if err != nil {
		return "(unmarshalable)"
	}
	s := string(b)
	if s == "null" || s == `{"ok":true}` {
		return "ok"
	}
	return truncate(s, bridgeCallSummaryCap)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type pingResponse struct {
	OK        bool   `json:"ok"`
	Version   string `json:"version"`
	MapLoaded bool   `json:"map_loaded"`
	MapName   string `json:"map_name,omitempty"`
	MapPath   string `json:"map_path,omitempty"`
	// Bridge identity — pid + port + shortened auth-token (first 8 hex). Lets
	// a connected MCP client self-identify WHICH wc3-forge window it's
	// talking to (multi-instance support — see internal/bridge/lockfile.go).
	// The Agent Console panel also reads these from App.GetBridgeInfo so the
	// in-page header bar matches what the wire reports.
	PID        int    `json:"pid"`
	Port       int    `json:"port"`
	TokenShort string `json:"token_short,omitempty"`
}

func handlePing(_ json.RawMessage) (any, error) {
	pid, port, tokenShort := BridgeIdentity()
	r := pingResponse{
		OK:         true,
		Version:    bridge.Version,
		MapLoaded:  Current.IsLoaded(),
		PID:        pid,
		Port:       port,
		TokenShort: tokenShort,
	}
	if r.MapLoaded {
		r.MapPath = Current.Path()
		if info := Current.Info(); info != nil {
			r.MapName = info.Name
		}
	}
	return r, nil
}

type mapOpenParams struct {
	Path string `json:"path"`
}

func handleMapOpen(params json.RawMessage) (any, error) {
	var p mapOpenParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Path == "" {
		return nil, errors.New("path is required")
	}
	if err := Current.Open(p.Path); err != nil {
		return nil, err
	}
	return mapStatusResult(), nil
}

func handleMapClose(_ json.RawMessage) (any, error) {
	Current.Close()
	return map[string]any{"ok": true}, nil
}

func handleMapStatus(_ json.RawMessage) (any, error) {
	return mapStatusResult(), nil
}

type mapStatusResponse struct {
	Loaded   bool   `json:"loaded"`
	Path     string `json:"path,omitempty"`
	Name     string `json:"name,omitempty"`
	UnitCount int   `json:"unit_count,omitempty"`
}

func mapStatusResult() mapStatusResponse {
	r := mapStatusResponse{Loaded: Current.IsLoaded()}
	if !r.Loaded {
		return r
	}
	r.Path = Current.Path()
	if info := Current.Info(); info != nil {
		r.Name = info.Name
	}
	if units := Current.Units(); units != nil {
		r.UnitCount = len(units.Entities)
	}
	return r
}

func handleMapInfoGet(_ json.RawMessage) (any, error) {
	info := Current.Info()
	if info == nil {
		return nil, errors.New("no map loaded")
	}
	return info, nil
}

// handleMapInfoSet applies a partial-update DTO to the in-memory war3map.w3i.
// Mirrors HiveWE's bridge surface: input is `{updates: {<key>: <value>, ...}}`,
// response is `{changed_fields: N}`. Routes through Session.MutateInfo so the
// shared dirty-tracking + entity-changed event wiring lights up just like the
// UI-driven path (App.MapInfoApply).
type mapInfoSetParams struct {
	Updates map[string]any `json:"updates"`
}

type mapInfoSetResponse struct {
	ChangedFields int `json:"changed_fields"`
}

func handleMapInfoSet(params json.RawMessage) (any, error) {
	var p mapInfoSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if len(p.Updates) == 0 {
		return mapInfoSetResponse{ChangedFields: 0}, nil
	}
	var changed int
	if err := Current.MutateInfo(func(info *w3i.Info) {
		changed = ApplyInfoUpdates(info, p.Updates)
	}); err != nil {
		return nil, err
	}
	return mapInfoSetResponse{ChangedFields: changed}, nil
}

// ApplyInfoUpdates is the shared implementation of the Map Info partial-update
// DTO walker, used by BOTH the Wails App.MapInfoApply method and the
// map.info_set MCP handler. Centralizing the switch here ensures both wire
// surfaces accept the exact same key set + value-type rules.
//
// Returns the number of fields successfully applied. Type-mismatches and
// unknown keys are silently skipped (counted as zero); callers that want
// strict validation should pre-validate the map.
//
// Wire keys (grouped by Map Info Editor tab):
//
//	Description:
//	  "name"                       string
//	  "author"                     string
//	  "description"                string
//	  "suggestedPlayers"           string
//
//	Loading Screen:
//	  "loadingScreenNumber"        int    (-1 for default/imported, ≥0 for campaign)
//	  "loadingScreenModel"         string (imported .mdx path; "" for default/campaign)
//	  "loadingScreenText"          string
//	  "loadingScreenTitle"         string
//	  "loadingScreenSubtitle"      string
//
//	Options (terrain/water/fog/flag bits):
//	  "meleeMap"                   bool   (Flags 0x000004)
//	  "hideMinimapPreview"         bool   (Flags 0x000001)
//	  "maskedAreaPartiallyVisible" bool   (Flags 0x000010)
//	  "cliffShoreWaves"            bool   (Flags 0x000800)
//	  "rollingShoreWaves"          bool   (Flags 0x001000)
//	  "itemClassification"         bool   (Flags 0x008000)
//	  "waterTinting"               bool   (Flags 0x010000)
//	  "waterColor"                 {r,g,b[,a]}  uint8 components
//	  "fogStyle"                   int    (0=off)
//	  "fogStartZ"                  number
//	  "fogEndZ"                    number
//	  "fogDensity"                 number
//	  "fogColor"                   {r,g,b[,a]}
//
//	Lighting & Weather:
//	  "weatherID"                  string (FourCC, e.g. "RAhr"; "" disables)
//	  "customSoundEnv"             string ("" disables)
//	  "customLightTileset"         string (single tileset letter, e.g. "L"; "" disables)
//
//	Size & Camera Bounds:
//	  "cameraComplements"          {left,right,bottom,top}  int32 tile counts
//
//	Advanced:
//	  "lua"                        bool   (v28+ marker)
//	  "gameDataSet"                int
//	  "supportedModes"             uint32
//	  "gameDataVersion"            uint32
//	  "camDistanceDefault"         uint32
//	  "camDistanceMax"             uint32
//	  "camDistanceMin"             uint32
//	  "forceDefaultZoom"           bool   (Flags 0x100000)
//	  "forceMaxZoom"               bool   (Flags 0x200000)
//	  "forceMinZoom"               bool   (Flags 0x400000)
//
// Adding a field: append a case to the switch + document the wire key here.
//
// IMPORTANT: when `name`/`author`/`description`/`suggestedPlayers` are
// updated, the value is written verbatim as the new on-disk string. If the
// loaded Info still carries the resolved-from-TRIGSTR display value (which it
// does today — Session.Open calls ResolveStrings post-Parse), an unrelated
// MapInfoApply call that touches OTHER fields will still write the literal
// strings on save because the original TRIGSTR tokens are lost in memory.
// See encode.go top-of-file for the full Option A vs Option B discussion.
func ApplyInfoUpdates(info *w3i.Info, updates map[string]any) int {
	var changed int
	// flagsDirty tracks whether we touched Flags.Raw so we only re-decode the
	// named-bool mirror once at the end (each named bool is just a cached
	// view of one bit of Raw — encode.go writes Raw directly).
	flagsDirty := false
	setBit := func(mask uint32, on bool) {
		if on {
			info.Flags.Raw |= mask
		} else {
			info.Flags.Raw &^= mask
		}
		flagsDirty = true
	}
	for key, raw := range updates {
		switch key {
		// --- Description ---
		case "name":
			if s, ok := raw.(string); ok {
				info.Name = s
				changed++
			}
		case "author":
			if s, ok := raw.(string); ok {
				info.Author = s
				changed++
			}
		case "description":
			if s, ok := raw.(string); ok {
				info.Description = s
				changed++
			}
		case "suggestedPlayers":
			if s, ok := raw.(string); ok {
				info.SuggestedPlayers = s
				changed++
			}

		// --- Loading Screen ---
		case "loadingScreenNumber":
			if n, ok := asInt32(raw); ok {
				info.LoadingScreen.Number = n
				changed++
			}
		case "loadingScreenModel":
			if s, ok := raw.(string); ok {
				info.LoadingScreen.Model = s
				changed++
			}
		case "loadingScreenText":
			if s, ok := raw.(string); ok {
				info.LoadingScreen.Text = s
				changed++
			}
		case "loadingScreenTitle":
			if s, ok := raw.(string); ok {
				info.LoadingScreen.Title = s
				changed++
			}
		case "loadingScreenSubtitle":
			if s, ok := raw.(string); ok {
				info.LoadingScreen.Subtitle = s
				changed++
			}

		// --- Options: flag-bit booleans ---
		case "meleeMap":
			if b, ok := raw.(bool); ok {
				setBit(0x000004, b)
				changed++
			}
		case "hideMinimapPreview":
			if b, ok := raw.(bool); ok {
				setBit(0x000001, b)
				changed++
			}
		case "maskedAreaPartiallyVisible":
			if b, ok := raw.(bool); ok {
				setBit(0x000010, b)
				changed++
			}
		case "cliffShoreWaves":
			if b, ok := raw.(bool); ok {
				setBit(0x000800, b)
				changed++
			}
		case "rollingShoreWaves":
			if b, ok := raw.(bool); ok {
				setBit(0x001000, b)
				changed++
			}
		case "itemClassification":
			if b, ok := raw.(bool); ok {
				setBit(0x008000, b)
				changed++
			}
		case "waterTinting":
			if b, ok := raw.(bool); ok {
				setBit(0x010000, b)
				changed++
			}

		// --- Options: water + fog ---
		case "waterColor":
			if c, ok := asColor(raw, info.WaterColor); ok {
				info.WaterColor = c
				changed++
			}
		case "fogStyle":
			if n, ok := asInt32(raw); ok {
				info.Fog.Style = n
				changed++
			}
		case "fogStartZ":
			if f, ok := asFloat32(raw); ok {
				info.Fog.StartZ = f
				changed++
			}
		case "fogEndZ":
			if f, ok := asFloat32(raw); ok {
				info.Fog.EndZ = f
				changed++
			}
		case "fogDensity":
			if f, ok := asFloat32(raw); ok {
				info.Fog.Density = f
				changed++
			}
		case "fogColor":
			if c, ok := asColor(raw, info.Fog.Color); ok {
				info.Fog.Color = c
				changed++
			}

		// --- Lighting & Weather ---
		case "weatherID":
			// Wire form: 4-char ASCII FourCC ("RAhr") packed little-endian
			// into int32, OR empty string to disable. Anything else: skip.
			if s, ok := raw.(string); ok {
				info.WeatherID = packFourCCToInt32(s)
				changed++
			}
		case "customSoundEnv":
			if s, ok := raw.(string); ok {
				info.CustomSoundEnv = s
				changed++
			}
		case "customLightTileset":
			// Wire form: single tileset character ("L", "A", ...), or "" /
			// "\0" to disable (stored as byte 0). HiveWE stores byte 0 to
			// mean "use the terrain's tileset" and reads back as null char.
			if s, ok := raw.(string); ok {
				var b byte
				if len(s) > 0 {
					b = s[0]
				}
				info.CustomLightTileset = b
				changed++
			}

		// --- Size & Camera Bounds ---
		case "cameraComplements":
			if v, ok := asIVec4(raw); ok {
				info.CameraComplements = v
				changed++
			}

		// --- Advanced ---
		case "lua":
			if b, ok := raw.(bool); ok {
				info.Lua = b
				changed++
			}
		case "gameDataSet":
			if n, ok := asInt32(raw); ok {
				info.GameDataSet = n
				changed++
			}
		case "supportedModes":
			if n, ok := asUint32(raw); ok {
				info.SupportedModes = n
				changed++
			}
		case "gameDataVersion":
			if n, ok := asUint32(raw); ok {
				info.GameDataVersion = n
				changed++
			}
		case "camDistanceDefault":
			if n, ok := asUint32(raw); ok {
				info.CamDistance.Default = n
				changed++
			}
		case "camDistanceMax":
			if n, ok := asUint32(raw); ok {
				info.CamDistance.Max = n
				changed++
			}
		case "camDistanceMin":
			if n, ok := asUint32(raw); ok {
				info.CamDistance.Min = n
				changed++
			}
		case "forceDefaultZoom":
			if b, ok := raw.(bool); ok {
				setBit(0x100000, b)
				changed++
			}
		case "forceMaxZoom":
			if b, ok := raw.(bool); ok {
				setBit(0x200000, b)
				changed++
			}
		case "forceMinZoom":
			if b, ok := raw.(bool); ok {
				setBit(0x400000, b)
				changed++
			}
		}
	}
	if flagsDirty {
		info.Flags = w3i.DecodeFlags(info.Flags.Raw)
	}
	return changed
}

// asInt32 coerces a JSON-decoded number (or numeric string) to int32. Returns
// false on type mismatch so the field is silently skipped by the caller, per
// the "lenient walker" contract of ApplyInfoUpdates.
func asInt32(raw any) (int32, bool) {
	switch v := raw.(type) {
	case float64:
		return int32(v), true
	case int:
		return int32(v), true
	case int32:
		return v, true
	case int64:
		return int32(v), true
	}
	return 0, false
}

func asUint32(raw any) (uint32, bool) {
	switch v := raw.(type) {
	case float64:
		if v < 0 {
			return 0, false
		}
		return uint32(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return uint32(v), true
	case int32:
		if v < 0 {
			return 0, false
		}
		return uint32(v), true
	case int64:
		if v < 0 {
			return 0, false
		}
		return uint32(v), true
	case uint32:
		return v, true
	}
	return 0, false
}

func asFloat32(raw any) (float32, bool) {
	switch v := raw.(type) {
	case float64:
		return float32(v), true
	case int:
		return float32(v), true
	case int32:
		return float32(v), true
	case int64:
		return float32(v), true
	}
	return 0, false
}

// asColor reads {r,g,b,a?} uint8 component values from a JSON-decoded map.
// Missing alpha defaults to the prior value's alpha so a Reforged .w3i that
// happens to ship a non-255 alpha isn't silently clobbered. Returns false on
// the wrong shape so the caller can skip the update.
func asColor(raw any, prior w3i.Color) (w3i.Color, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return w3i.Color{}, false
	}
	get := func(key string) (uint8, bool) {
		v, ok := m[key]
		if !ok {
			return 0, false
		}
		switch n := v.(type) {
		case float64:
			if n < 0 {
				return 0, true
			}
			if n > 255 {
				return 255, true
			}
			return uint8(n), true
		}
		return 0, false
	}
	r, okR := get("r")
	g, okG := get("g")
	b, okB := get("b")
	if !okR || !okG || !okB {
		return w3i.Color{}, false
	}
	a, okA := get("a")
	if !okA {
		a = prior.A
	}
	return w3i.Color{R: r, G: g, B: b, A: a}, true
}

func asIVec4(raw any) (w3i.IVec4, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return w3i.IVec4{}, false
	}
	get := func(key string) (int32, bool) {
		v, ok := m[key]
		if !ok {
			return 0, false
		}
		return asInt32(v)
	}
	// HiveWE's w3i CameraComplements convention: A=left, B=right, C=bottom,
	// D=top. Keep that mapping on the wire so MCP and UI both agree.
	l, ok1 := get("left")
	r, ok2 := get("right")
	b, ok3 := get("bottom")
	t, ok4 := get("top")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return w3i.IVec4{}, false
	}
	return w3i.IVec4{A: l, B: r, C: b, D: t}, true
}

// packFourCCToInt32 packs the first 4 bytes of s into a little-endian int32,
// matching HiveWE's `*reinterpret_cast<int*>(name.data())` trick for the
// weather_id field. Empty / short strings zero-pad on the right; values
// longer than 4 chars are truncated.
func packFourCCToInt32(s string) int32 {
	var b [4]byte
	for i := 0; i < 4 && i < len(s); i++ {
		b[i] = s[i]
	}
	return int32(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
}

// Int32ToFourCC is the inverse of packFourCCToInt32 — exported so the Wails
// surface can expose the WeatherID as a human-readable FourCC. Trailing nulls
// (the "no weather" case where the field is zero) are trimmed so the JS side
// gets "" rather than four NUL bytes.
func Int32ToFourCC(v int32) string {
	if v == 0 {
		return ""
	}
	u := uint32(v)
	b := []byte{byte(u), byte(u >> 8), byte(u >> 16), byte(u >> 24)}
	// Trim trailing nulls in case the wire value was short.
	end := len(b)
	for end > 0 && b[end-1] == 0 {
		end--
	}
	return string(b[:end])
}

// unitsListResponse mirrors the C++ fork's shape: each entity is rendered with
// game-coordinate position + the core identity fields a tool needs. Skin/level/
// HP/mana/inventory included; the heavier ability-modifications + random-data
// blobs are returned only on units.get (TODO).
type unitsListEntity struct {
	CreationNumber uint32     `json:"creation_number"`
	TypeID         string     `json:"type_id"`
	SkinID         string     `json:"skin_id,omitempty"`
	Player         uint32     `json:"player"`
	Position       [3]float32 `json:"position"`
	Rotation       float32    `json:"rotation"`
	Scale          [3]float32 `json:"scale"`
	HitPointsPct   int32      `json:"hit_points_pct"`
	ManaPct        int32      `json:"mana_pct"`
	HeroLevel      uint32     `json:"hero_level,omitempty"`
	GoldAmount     uint32     `json:"gold_amount,omitempty"`
	Inventory      []invItem  `json:"inventory,omitempty"`
}

type invItem struct {
	Slot   uint32 `json:"slot"`
	ItemID string `json:"item_id"`
}

func handleSelectionGet(_ json.RawMessage) (any, error) {
	return Current.Selection(), nil
}

type selectionSetParams struct {
	Items []SelectionItem `json:"items"`
}

func handleSelectionSet(params json.RawMessage) (any, error) {
	var p selectionSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	primary := len(p.Items) - 1 // most recently added becomes primary
	Current.SetSelection(p.Items, primary)
	return Current.Selection(), nil
}

func handleSelectionClear(_ json.RawMessage) (any, error) {
	Current.SetSelection(nil, -1)
	return Current.Selection(), nil
}

// unitsMoveParams carries game-coordinate position for a single unit. x/y/z
// are WC3 game coords (centered at 0,0) — the SAME wire contract as the Wails
// App.MoveUnit method. No conversion happens here; Session.MoveUnit stores
// Position verbatim.
type unitsMoveParams struct {
	CreationNumber uint32  `json:"creation_number"`
	X              float32 `json:"x"`
	Y              float32 `json:"y"`
	Z              float32 `json:"z"`
}

type unitsMoveResponse struct {
	OK             bool       `json:"ok"`
	CreationNumber uint32     `json:"creation_number"`
	Position       [3]float32 `json:"position"`
}

func handleUnitsMove(params json.RawMessage) (any, error) {
	var p unitsMoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.MoveUnit(p.CreationNumber, p.X, p.Y, p.Z); err != nil {
		return nil, err
	}
	return unitsMoveResponse{
		OK:             true,
		CreationNumber: p.CreationNumber,
		Position:       [3]float32{p.X, p.Y, p.Z},
	}, nil
}

// handleDoodadsMove is the doodad parallel to handleUnitsMove. The wire
// shape is identical (creation_number + x/y/z game coords); the difference
// is purely the dispatch target — Session.MoveDoodad mutates war3map.doo
// instead of war3mapUnits.doo. Reuses the unitsMoveParams/unitsMoveResponse
// structs because the shape is byte-for-byte the same; only the method name
// on the wire (`doodads.move` vs `units.move`) disambiguates kind.
func handleDoodadsMove(params json.RawMessage) (any, error) {
	var p unitsMoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.MoveDoodad(p.CreationNumber, p.X, p.Y, p.Z); err != nil {
		return nil, err
	}
	return unitsMoveResponse{
		OK:             true,
		CreationNumber: p.CreationNumber,
		Position:       [3]float32{p.X, p.Y, p.Z},
	}, nil
}

// unitsRotateParams carries a single-axis rotation for the units.rotate handler.
// Rotation is in radians around the Z axis (the only axis WC3 units support).
type unitsRotateParams struct {
	CreationNumber uint32  `json:"creation_number"`
	Rotation       float32 `json:"rotation"` // radians, Z-axis only
}

type unitsRotateResponse struct {
	OK             bool    `json:"ok"`
	CreationNumber uint32  `json:"creation_number"`
	Rotation       float32 `json:"rotation"`
}

// handleUnitsRotate applies a new facing angle to the unit with the given
// creation_number. Wraps Session.RotateUnit; mirrors handleUnitsMove shape.
func handleUnitsRotate(params json.RawMessage) (any, error) {
	var p unitsRotateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.RotateUnit(p.CreationNumber, p.Rotation); err != nil {
		return nil, err
	}
	return unitsRotateResponse{
		OK:             true,
		CreationNumber: p.CreationNumber,
		Rotation:       p.Rotation,
	}, nil
}

// handleDoodadsRotate applies a new facing angle to the doodad with the given
// creation_number. Reuses unitsRotateParams/unitsRotateResponse because the
// wire shape is identical; only the dispatch target differs.
func handleDoodadsRotate(params json.RawMessage) (any, error) {
	var p unitsRotateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.RotateDoodad(p.CreationNumber, p.Rotation); err != nil {
		return nil, err
	}
	return unitsRotateResponse{
		OK:             true,
		CreationNumber: p.CreationNumber,
		Rotation:       p.Rotation,
	}, nil
}

// unitsScaleParams carries per-axis scale for the units.scale and doodads.scale
// handlers. For units, sx/sy/sz are runtime-normalized (1.0 = default). For
// doodads, they are raw on-disk values (same convention — 1.0 = default, but
// doodads store them verbatim without the /128 divide units use).
type unitsScaleParams struct {
	CreationNumber uint32  `json:"creation_number"`
	Sx             float32 `json:"sx"`
	Sy             float32 `json:"sy"`
	Sz             float32 `json:"sz"`
}

type unitsScaleResponse struct {
	OK             bool       `json:"ok"`
	CreationNumber uint32     `json:"creation_number"`
	Scale          [3]float32 `json:"scale"`
}

// handleUnitsScale sets per-axis scale on the unit with the given
// creation_number. Session.ScaleUnit clears scaleRaw so Encode emits the new
// value; see feedback_unitsdoo_scale_raw.md for the gotcha background.
func handleUnitsScale(params json.RawMessage) (any, error) {
	var p unitsScaleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.ScaleUnit(p.CreationNumber, p.Sx, p.Sy, p.Sz); err != nil {
		return nil, err
	}
	return unitsScaleResponse{
		OK:             true,
		CreationNumber: p.CreationNumber,
		Scale:          [3]float32{p.Sx, p.Sy, p.Sz},
	}, nil
}

// handleDoodadsScale sets per-axis scale on the doodad with the given
// creation_number. Doodad scale is stored raw on disk — no scaleRaw
// invalidation needed; Encode writes Scale verbatim.
func handleDoodadsScale(params json.RawMessage) (any, error) {
	var p unitsScaleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := Current.ScaleDoodad(p.CreationNumber, p.Sx, p.Sy, p.Sz); err != nil {
		return nil, err
	}
	return unitsScaleResponse{
		OK:             true,
		CreationNumber: p.CreationNumber,
		Scale:          [3]float32{p.Sx, p.Sy, p.Sz},
	}, nil
}

// handleMapSave flushes pending edits to disk via the session's source. For
// MPQ-backed sessions, returns a user-visible message instructing the caller
// to extract the map to a folder first; programmatic clients can also check
// the wrapped sentinel through the standard JSON-RPC error structure.
func handleMapSave(_ json.RawMessage) (any, error) {
	if err := Current.Save(); err != nil {
		if errors.Is(err, ErrMPQWriteNotImplemented) {
			return nil, errors.New("MPQ archive saving is not yet implemented — extract the map to a folder first.")
		}
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// unitsListParams supports an optional filter on the units.list response so
// agents driving busy maps don't have to swallow the full 200+ entity payload
// every call. Both filter fields are pointers so "absent" is distinguishable
// from a meaningful zero (Player=0 is a real slot; TypeID="" is the absent
// marker but kept symmetric for cleanliness).
//
// Wire shape mirrors HiveWE's `{filter: {type_id, player}}` so agent scripts
// written for either editor work against the other. `include_items` from the
// HiveWE shape is intentionally omitted — wc3-forge's units_list never mixes
// item entities in to begin with (items live as inventory slots on units).
type unitsListParams struct {
	Filter *struct {
		TypeID *string `json:"type_id"`
		Player *uint32 `json:"player"`
	} `json:"filter"`
}

func handleUnitsList(params json.RawMessage) (any, error) {
	units := Current.Units()
	if units == nil {
		return nil, errors.New("no map loaded")
	}
	var p unitsListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	out := make([]unitsListEntity, 0, len(units.Entities))
	for _, e := range units.Entities {
		if p.Filter != nil {
			if p.Filter.TypeID != nil && e.TypeID != *p.Filter.TypeID {
				continue
			}
			if p.Filter.Player != nil && e.Player != *p.Filter.Player {
				continue
			}
		}
		inv := make([]invItem, 0, len(e.Inventory))
		for _, slot := range e.Inventory {
			inv = append(inv, invItem{Slot: slot.Slot, ItemID: slot.ItemID})
		}
		out = append(out, unitsListEntity{
			CreationNumber: e.CreationNumber,
			TypeID:         e.TypeID,
			SkinID:         e.SkinID,
			Player:         e.Player,
			Position:       e.Position,
			Rotation:       e.Rotation,
			Scale:          e.Scale,
			HitPointsPct:   e.HitPointsPct,
			ManaPct:        e.ManaPct,
			HeroLevel:      e.HeroLevel,
			GoldAmount:     e.GoldAmount,
			Inventory:      inv,
		})
	}
	return map[string]any{"entities": out}, nil
}

// handleUnitsGet returns the full unitsdoo.Entity for one creation_number —
// the same depth the Properties panel uses (Inventory, ItemDrops,
// AbilityModifications, Hero stats). Mirrors App.GetUnit.
type unitsGetParams struct {
	CreationNumber uint32 `json:"creation_number"`
}

func handleUnitsGet(params json.RawMessage) (any, error) {
	var p unitsGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	units := Current.Units()
	if units == nil {
		return nil, errors.New("no map loaded")
	}
	for i := range units.Entities {
		if units.Entities[i].CreationNumber == p.CreationNumber {
			return &units.Entities[i], nil
		}
	}
	return nil, fmt.Errorf("no unit with creation_number %d", p.CreationNumber)
}

// doodadsListEntity is the bridge-shape DTO for doodads.list. Trim of the
// full doodadsdoo.Doodad — matches the UI's ListDoodads payload (creation
// number, type id, position, rotation, scale, variation, life, flags) so the
// MCP and Wails surfaces stay in lockstep.
type doodadsListEntity struct {
	CreationNumber uint32     `json:"creation_number"`
	TypeID         string     `json:"type_id"`
	SkinID         string     `json:"skin_id,omitempty"`
	Position       [3]float32 `json:"position"`
	Rotation       float32    `json:"rotation"`
	Scale          [3]float32 `json:"scale"`
	Variation      uint32     `json:"variation"`
	Life           uint8      `json:"life"`
	Flags          uint8      `json:"flags"`
}

// handleDoodadsList enumerates placed doodads + destructibles. Mirrors
// App.ListDoodads — doodads outnumber units 50:1 in real maps so this is the
// most-used "what's in the map" handler for any non-trivial agent workflow.
func handleDoodadsList(_ json.RawMessage) (any, error) {
	dd := Current.Doodads()
	if dd == nil {
		return nil, errors.New("no map loaded")
	}
	out := make([]doodadsListEntity, 0, len(dd.Doodads))
	for _, d := range dd.Doodads {
		out = append(out, doodadsListEntity{
			CreationNumber: d.CreationNumber,
			TypeID:         d.TypeID,
			SkinID:         d.SkinID,
			Position:       d.Position,
			Rotation:       d.Rotation,
			Scale:          d.Scale,
			Variation:      d.Variation,
			Life:           d.Life,
			Flags:          d.Flags,
		})
	}
	return map[string]any{"doodads": out}, nil
}

// handleDoodadsGet returns the full doodadsdoo.Doodad for one creation_number.
// Mirrors App.GetDoodad — depth needed for the Properties panel (item drops,
// per-doodad flags, raw scale bits).
type doodadsGetParams struct {
	CreationNumber uint32 `json:"creation_number"`
}

func handleDoodadsGet(params json.RawMessage) (any, error) {
	var p doodadsGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	dd := Current.Doodads()
	if dd == nil {
		return nil, errors.New("no map loaded")
	}
	for i := range dd.Doodads {
		if dd.Doodads[i].CreationNumber == p.CreationNumber {
			return &dd.Doodads[i], nil
		}
	}
	return nil, fmt.Errorf("no doodad with creation_number %d", p.CreationNumber)
}

// _ keeps doodadsdoo/unitsdoo imports referenced even if the returned values
// happen to live entirely behind pointers. Compile-time guard against the
// imports being silently dropped by goimports if future refactors remove
// every direct mention.
var _ = doodadsdoo.File{}
var _ = unitsdoo.File{}

// handleViewSetMode toggles the editor's terrain/doodad pick mode. The
// authoritative state lives in JS (App.svelte's `terrainPickModeOn`), so the
// handler funnels through the existing wc3-forge:test-command event bus.
//
// The current shape of the JS handler is a TOGGLE, not a SET — so the MCP
// call only emits the toggle when the requested mode differs from the
// current one would require a query. To keep the handler simple we treat
// `terrain` and `doodad` modes as a toggle target: every call emits a single
// toggle which flips the mode. Callers needing idempotency should read
// current state via the (not-yet-existent) `view.get_mode` or just inspect
// the streamed wc3-forge:selection-changed/etc. events.
type viewSetModeParams struct {
	Mode string `json:"mode"` // "terrain" | "doodad" — informational; the toggle event flips state regardless
}

func handleViewSetMode(params json.RawMessage) (any, error) {
	var p viewSetModeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	switch p.Mode {
	case "", "terrain", "doodad":
		// Accepted values — informational only; the JS receiver flips state.
	default:
		return nil, fmt.Errorf("mode must be 'terrain' or 'doodad' (got %q)", p.Mode)
	}
	Current.EmitUICommand("terrain.toggle")
	return map[string]any{"ok": true, "mode_requested": p.Mode}, nil
}

// handleViewSetDoodadCategoryVisible mirrors the View menu's per-category
// visibility checkbox. Routes through EmitUICommand so the existing
// `doodad.toggle <cat>` test-command handler in App.svelte applies the
// change to the scene.
//
// Quirk: the JS handler ignores the `visible` field and TOGGLES the current
// state. For now we forward the call regardless — agents calling this in a
// loop will see the value oscillate, but the single-call case (the common
// one) lands correctly.
type viewSetDoodadCategoryVisibleParams struct {
	Category string `json:"category"`           // "Trees/Destructibles", "Structures", … or "*" for all
	Visible  bool   `json:"visible"`            // informational; the underlying JS handler toggles
}

func handleViewSetDoodadCategoryVisible(params json.RawMessage) (any, error) {
	var p viewSetDoodadCategoryVisibleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Category == "" {
		return nil, errors.New("category is required")
	}
	Current.EmitUICommand("doodad.toggle " + p.Category)
	return map[string]any{"ok": true, "category": p.Category, "visible_requested": p.Visible}, nil
}

// handleCameraSetView pans the camera pivot to (x, y, z) and optionally sets
// distance. Mirrors the --camera CLI flag's spec format. Surfaces through
// the camera bus (separate from test-command — it's a structured payload
// the App layer turns into a `wc3-forge:startup-camera` event).
//
// The startup-camera event is what the JS uses on first map-load to apply
// the --camera flag; reusing it for runtime calls keeps the JS-side handler
// path single (it always pans to the spec + sets distance when distance>0).
type cameraSetViewParams struct {
	X        float32 `json:"x"`
	Y        float32 `json:"y"`
	Z        float32 `json:"z"`
	Distance float32 `json:"distance"`
}

func handleCameraSetView(params json.RawMessage) (any, error) {
	var p cameraSetViewParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	// Reuse the camera-spec text format so the JS receiver's existing parser
	// works without modification. Distance omitted when zero — same convention
	// the --camera CLI flag uses.
	spec := fmt.Sprintf("%g,%g,%g", p.X, p.Y, p.Z)
	if p.Distance > 0 {
		spec = fmt.Sprintf("%g,%g,%g,%g", p.X, p.Y, p.Z, p.Distance)
	}
	Current.EmitUICommand("camera.set " + spec)
	return map[string]any{"ok": true, "spec": spec}, nil
}

// windowSetTitleParams carries the free-form agent label that gets composed
// into the OS window title. Empty string clears the label (title falls back
// to "<map> — PID <n>" or "wc3-forge — PID <n>").
type windowSetTitleParams struct {
	Label string `json:"label"`
}

type windowSetTitleResponse struct {
	OK    bool   `json:"ok"`
	Label string `json:"label"`
}

// handleWindowSetTitle updates the session's agent label. The App layer
// listens on OnAgentLabelChanged and re-derives the OS-visible Wails title.
// No truncation here: the App-side composer (and the OS taskbar) decide how
// much of a long label to surface; agents can self-discipline by sending
// short strings.
func handleWindowSetTitle(params json.RawMessage) (any, error) {
	var p windowSetTitleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	Current.SetAgentLabel(p.Label)
	return windowSetTitleResponse{OK: true, Label: p.Label}, nil
}

// handleHistoryUndo reverts the most recent mutation. Returns {ok, label}
// where label is the human-readable description of what was undone (empty
// when the history stack was already empty — the call is a no-op).
func handleHistoryUndo(params json.RawMessage) (any, error) {
	state := Current.HistoryList()
	label := ""
	if n := len(state.Undo); n > 0 {
		label = state.Undo[n-1].Label
	}
	if err := Current.Undo(); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "label": label}, nil
}

// handleHistoryRedo re-applies the most-recently-undone mutation.
func handleHistoryRedo(params json.RawMessage) (any, error) {
	state := Current.HistoryList()
	label := ""
	if n := len(state.Redo); n > 0 {
		label = state.Redo[n-1].Label
	}
	if err := Current.Redo(); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "label": label}, nil
}

// handleHistoryList returns the current undo/redo stacks (oldest-first).
// Agents use this to surface the editor's history state without keeping
// their own shadow copy.
func handleHistoryList(params json.RawMessage) (any, error) {
	return Current.HistoryList(), nil
}

// handleHistoryBeginGroup starts an undo transaction. All subsequent
// mutations until the matching history.end_group land in one undo step.
// Idempotent re-entry is supported (nested begins increment depth).
type historyGroupParams struct {
	Label string `json:"label"`
}

func handleHistoryBeginGroup(params json.RawMessage) (any, error) {
	var p historyGroupParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	Current.BeginUndoGroup(p.Label)
	return map[string]any{"ok": true, "label": p.Label}, nil
}

// handleHistoryEndGroup closes the outermost undo group (or decrements
// nesting). Returns an error if called without a matching begin.
func handleHistoryEndGroup(params json.RawMessage) (any, error) {
	if err := Current.EndUndoGroup(); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

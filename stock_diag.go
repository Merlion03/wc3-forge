package main

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/StephenSHorton/wc3-forge/internal/forge"
)

// Stock-data cache poisoning visibility, surfaced under the "stockCaches"
// namespace of diagnostics.get's "go" key (pull-only). The process-wide stock
// loaders (terrain/water/cliff SLK tables, the unit/doodad type index, command
// buttons, editor option lists) are sync.Once memoizers: the first call wins
// and its result — success OR a MISS — sticks for the process lifetime (see
// resetStockDataCaches in stockcache.go). When wc3-forge starts with no valid
// install, those first calls memoize an error, and every downstream consumer
// (flat-colored terrain, no cliffs/water, empty option lists) inherits the
// poison until a CASC remount resets the Once.
//
// This provider turns "terrain renders flat: renderer bug or poisoned SLK
// cache?" into one read. For each cache it reports {loaded, error}, where
// `loaded` means the guarding sync.Once has ALREADY fired (we never force a
// load — onceFired only inspects the Once's internal done flag) and `error` is
// the stored sentinel. A non-empty error is a poisoned cache; `poisoned` is the
// roll-up so the overlay flags it. Reads sentinels + Once flags only; touches
// no map mutex and never forge.Current.mu (diagprov.go invariant).
//
// Honesty caveat: a cache whose loader stores neither a value nor an error on
// success (the option-list loaders) reports {loaded:true, error:""} once its
// Once has fired — exactly the desired signal. The *Err-bearing loaders
// (terrain/water/cliff/typeindex/commandButtons) additionally surface the
// failure string, which is the high-value case for SLK poisoning.

func init() {
	forge.RegisterGoDiag("stockCaches", func() any { return getStockCacheState() })
}

// stockCacheEntry is one cache's reported state.
type stockCacheEntry struct {
	Loaded bool   `json:"loaded"`          // the guarding sync.Once has already fired
	Error  string `json:"error,omitempty"` // stored *Err sentinel, if any (poison)
}

// getStockCacheState reads the stock-data cache sentinels + Once flags WITHOUT
// forcing any load, returning cacheName -> {loaded, error} plus a `poisoned`
// roll-up listing every cache that fired with an error sentinel. Safe to call
// concurrently with the loaders: it only reads (atomically, for the Once flag)
// and copies error interfaces, never mutating.
func getStockCacheState() map[string]any {
	caches := map[string]stockCacheEntry{
		// tileset_colors.go — the three SLK tables that map tile/cliff/water
		// FourCCs to texture paths. A poisoned terrainSLK is the classic
		// "terrain renders flat-colored" root cause.
		"terrainSLK":    {Loaded: onceFired(&terrainSLKOnce), Error: errStr(terrainSLKErr)},
		"waterSLK":      {Loaded: onceFired(&waterSLKOnce), Error: errStr(waterSLKErr)},
		"cliffTypesSLK": {Loaded: onceFired(&cliffTypesSLKOnce), Error: errStr(cliffTypesSLKErr)},

		// typeindex.go — unit/doodad type tables (Explorer labels + object-editor
		// display data). indexErr poison => empty/garbled type names everywhere.
		"typeIndex": {Loaded: onceFired(&indexOnce), Error: errStr(indexErr)},

		// app.go — command-button icon enumeration (object-editor icon picker).
		"commandButtons": {Loaded: onceFired(&commandButtonsOnce), Error: errStr(commandButtonsErr)},

		// The remaining stock loaders store no error sentinel; we report only
		// whether their Once has fired (a fired-but-empty list is itself a hint
		// the loader saw no install). No error string to surface.
		"worldEditStrings": {Loaded: onceFired(&wesOnce)},
		"doodadIcons":      {Loaded: onceFired(&doodadIconOnce)},
		"weatherOptions":   {Loaded: onceFired(&weatherOnce)},
		"soundEnvOptions":  {Loaded: onceFired(&soundEnvOnce)},
		"tilesetOptions":   {Loaded: onceFired(&tilesetOnce)},
		"campaignLoading":  {Loaded: onceFired(&campaignLoadingOnce)},
		"skyModels":        {Loaded: onceFired(&skyModelsOnce)},
	}

	// Roll-up: any fired cache carrying an error sentinel is poisoned. Named with
	// the count so the overlay auto-red-tints a nonzero value.
	poisoned := []string{}
	for name, e := range caches {
		if e.Loaded && e.Error != "" {
			poisoned = append(poisoned, name)
		}
	}

	out := make(map[string]any, len(caches)+1)
	for name, e := range caches {
		out[name] = e
	}
	out["poisonedCaches"] = poisoned
	return out
}

// onceFired reports whether o.Do has already run, WITHOUT triggering it. It
// reads the Once's internal done flag — the first non-zero-size field of
// sync.Once is an atomic.Bool (`done`) at offset 0 on this Go toolchain (1.26)
// — via an unsafe cast. This is the only way to honestly distinguish "loader
// has run" from "never touched" without calling o.Do (which would force the
// very load this diagnostic must not trigger). A read-only atomic load; never
// mutates the Once.
func onceFired(o *sync.Once) bool {
	done := (*atomic.Bool)(unsafe.Pointer(o))
	return done.Load()
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

package main

import "sync"

// resetStockDataCaches clears every process-wide cache that memoizes data read
// from the WC3 install via readBaseAsset (stock SLK tables, tileset colors and
// thumbnails, editor option lists, the type index, command-button icons).
//
// These loaders are sync.Once / map memoizers: the first call wins and the
// result — success OR failure — sticks for the process lifetime. That's fine
// normally, but when wc3-forge starts with no valid install the first calls
// memoize a MISS, and after the user locates their install via
// SetWC3InstallPath -> reopenCASC the loaders would keep returning that cached
// miss (flat-color terrain, no cliffs/water textures, empty option lists)
// until a restart. Resetting the Once + nilling the cached value forces the
// next call to re-read against the freshly mounted storage.
//
// Called only from reopenCASC (a deliberate, user-initiated remount), so no
// loader is mid-flight: the initial lazy loads completed long before and the
// render loop draws already-built GL data rather than re-entering these
// loaders. The frontend's resourceMap purge + map reload (driven by the
// eventCASCRemounted handler) happens AFTER this returns, so the re-fetch sees
// the cleared caches.
func resetStockDataCaches() {
	// tileset_colors.go — terrain ground colors + the three SLK tables that
	// map tile/cliff/water FourCCs to their texture paths (the terrain render's
	// path source; a cached miss here is what leaves terrain flat-colored).
	tileColorMu.Lock()
	tileColorCache = map[string][3]uint8{}
	tileColorMu.Unlock()
	terrainSLK, terrainSLKErr, terrainSLKOnce = nil, nil, sync.Once{}
	waterSLK, waterSLKErr, waterSLKOnce = nil, nil, sync.Once{}
	cliffTypesSLK, cliffTypesSLKErr, cliffTypesSLKOnce = nil, nil, sync.Once{}

	// tileset_thumbs.go — tileset-swatch thumbnails (Map Info tileset picker).
	tileThumbMu.Lock()
	tileThumbCache = map[string]string{}
	tileThumbMu.Unlock()

	// mapinfo_options.go — Map Info dropdown option lists sourced from stock
	// SLK / WorldEditData.txt.
	weatherCache, weatherOnce = nil, sync.Once{}
	soundEnvCache, soundEnvOnce = nil, sync.Once{}
	tilesetCache, tilesetOnce = nil, sync.Once{}
	campaignLoadingCache, campaignLoadingOnce = nil, sync.Once{}

	// sky.go — Map Info sky-model option list.
	skyModelsCache, skyModelsOnce = nil, sync.Once{}

	// typeindex.go — unit/doodad type tables, WorldEditStrings, doodad
	// category icons (Explorer labels + object-editor display data).
	indexErr, unitIndex, doodIndex, indexOnce = nil, nil, nil, sync.Once{}
	wesTab, wesOnce = nil, sync.Once{}
	doodadCategoryIcons, doodadIconOnce = nil, sync.Once{}

	// app.go — command-button icon enumeration (object-editor icon picker).
	commandButtonsList, commandButtonsErr, commandButtonsOnce = nil, nil, sync.Once{}
}

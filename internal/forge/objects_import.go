package forge

// Cross-map object importer. Copies custom objects (abilities/units/buffs/items/
// upgrades/destructables/doodads) from a SOURCE map into the currently-loaded
// map, faithfully — including the dependency graph: an ability's referenced
// buffs, the units it summons, those units' abilities, and any imported icon/
// model files. The source map is read through a standalone fileSource and never
// loaded into the Session, so the active map is untouched until we mutate it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/StephenSHorton/wc3-forge/internal/formats/mpq"
	"github.com/StephenSHorton/wc3-forge/internal/formats/w3objmod"
)

// ImportRequest names one object to import, by kind + FourCC.
type ImportRequest struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ImportObjectsResult reports the outcome.
type ImportObjectsResult struct {
	Imported      []string `json:"imported"`       // "kind:id" newly created
	Skipped       []string `json:"skipped"`        // already present in the target
	Missing       []string `json:"missing"`        // requested/referenced but not a custom in source
	ImportedFiles []string `json:"imported_files"` // imported asset paths copied across
	Failed        []string `json:"failed"`         // "kind:id: reason"
}

type objRef struct{ kind, id string }

// openSourceReader opens a read-only fileSource over a .w3x/.w3m/.mpq or an
// extracted folder. Mirrors Session.Open's source selection without swapping
// any Session state.
func openSourceReader(path string) (fileSource, func(), error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve path: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("stat %q: %w", abs, err)
	}
	if fi.IsDir() {
		return folderSource{root: abs}, func() {}, nil
	}
	archive, err := mpq.Open(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("open MPQ %q: %w", abs, err)
	}
	src := newMPQSource(archive, abs)
	return src, func() { _ = src.close() }, nil
}

// sourceObjects holds a source map's parsed custom objects, indexed kind -> id.
type sourceObjects struct {
	customs map[string]map[string]*w3objmod.CustomObject
}

func loadSourceObjects(src fileSource) *sourceObjects {
	so := &sourceObjects{customs: map[string]map[string]*w3objmod.CustomObject{}}
	for kind, cfg := range kindConfigs {
		b, ok, err := src.read(cfg.ShadowFile)
		if err != nil || !ok || len(b) == 0 {
			continue
		}
		f, err := w3objmod.Parse(b, cfg.ShadowOpt, nil) // nil fields => raw FourCC keys
		if err != nil || f == nil {
			continue
		}
		idx := make(map[string]*w3objmod.CustomObject, len(f.Customs))
		for i := range f.Customs {
			idx[f.Customs[i].ID] = &f.Customs[i]
		}
		so.customs[kind] = idx
	}
	return so
}

func (so *sourceObjects) custom(kind, id string) *w3objmod.CustomObject {
	if m := so.customs[kind]; m != nil {
		return m[id]
	}
	return nil
}

// kindForFieldType maps a field metadata Type to the object kind its values
// reference (comma-separated FourCCs), or "" if it is not an object reference.
func kindForFieldType(t string) string {
	switch t {
	case "abilityList", "abilitySkinList":
		return "abilities"
	case "unitList":
		return "units"
	case "buffList":
		return "buffs"
	case "upgradeList":
		return "upgrades"
	case "destructableList":
		return "destructables"
	case "doodadList":
		return "doodads"
	case "itemList":
		return "items"
	}
	return ""
}

// ImportObjectsFromMap copies the requested objects plus their custom-object and
// imported-file dependencies from srcPath into the loaded map. Objects already
// present in the target are left untouched (reported as skipped). The whole copy
// is one undo group.
func (s *Session) ImportObjectsFromMap(srcPath string, reqs []ImportRequest) (*ImportObjectsResult, error) {
	if Current.Info() == nil {
		return nil, errors.New("no map loaded")
	}
	src, closeSrc, err := openSourceReader(srcPath)
	if err != nil {
		return nil, err
	}
	defer closeSrc()
	so := loadSourceObjects(src)

	res := &ImportObjectsResult{}
	visited := map[string]bool{}
	importedFiles := map[string]bool{}

	queue := make([]objRef, 0, len(reqs))
	for _, r := range reqs {
		queue = append(queue, objRef{r.Kind, r.ID})
	}

	s.BeginUndoGroup(fmt.Sprintf("Import %d objects from map", len(reqs)))
	defer func() { _ = s.EndUndoGroup() }()

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		key := cur.kind + ":" + cur.id
		if visited[key] {
			continue
		}
		visited[key] = true

		cfg := kindConfigFor(cur.kind)
		if cfg == nil {
			res.Failed = append(res.Failed, key+": unknown kind")
			continue
		}
		c := so.custom(cur.kind, cur.id)
		if c == nil {
			// Not custom in source: either a stock id (already in our base) or
			// genuinely absent. Either way nothing to copy.
			res.Missing = append(res.Missing, key)
			continue
		}
		if objectExistsInTarget(s, cfg, cur.id) {
			res.Skipped = append(res.Skipped, key)
			continue
		}
		if _, err := s.AddCustomObject(cfg, cur.id, c.BaseID); err != nil {
			res.Failed = append(res.Failed, key+": "+err.Error())
			continue
		}
		_, meta, _ := loadObjectBase(cfg)

		for fourCC, val := range c.Overrides {
			_ = s.SetObjectField(cfg, cur.id, fourCC, val)
			collectDeps(s, so, meta, src, fourCC, val, &queue, importedFiles, res)
		}
		for _, lv := range c.Levels {
			_ = s.SetObjectFieldLevel(cfg, cur.id, lv.FourCC, lv.Level, lv.Value)
			collectDeps(s, so, meta, src, lv.FourCC, lv.Value, &queue, importedFiles, res)
		}
		res.Imported = append(res.Imported, key)
	}
	return res, nil
}

// collectDeps inspects one (field, value) pair: queues referenced custom objects
// and copies referenced imported files.
func collectDeps(s *Session, so *sourceObjects, meta *ObjectMetadata, src fileSource,
	fourCC, val string, queue *[]objRef, importedFiles map[string]bool, res *ImportObjectsResult) {
	if meta == nil {
		return
	}
	fm := meta.ByID[fourCC]
	if fm == nil {
		return
	}
	switch fm.Type {
	case "icon", "model":
		for _, p := range filePathCandidates(val) {
			if importedFiles[p] {
				continue
			}
			if b, ok, _ := src.read(p); ok && len(b) > 0 {
				if _, err := s.AddImportFile(p, b); err == nil {
					importedFiles[p] = true
					res.ImportedFiles = append(res.ImportedFiles, p)
				}
			}
		}
	case "techList":
		// requirements: a mixed unit/upgrade FourCC list.
		for _, tok := range splitFourCCs(val) {
			for _, k := range []string{"units", "upgrades"} {
				if so.custom(k, tok) != nil {
					*queue = append(*queue, objRef{k, tok})
				}
			}
		}
	default:
		if k := kindForFieldType(fm.Type); k != "" {
			for _, tok := range splitFourCCs(val) {
				if so.custom(k, tok) != nil {
					*queue = append(*queue, objRef{k, tok})
				}
			}
		}
	}
}

func objectExistsInTarget(s *Session, cfg *KindConfig, id string) bool {
	if mods := cfg.GetMods(s); mods != nil {
		for i := range mods.Customs {
			if mods.Customs[i].ID == id {
				return true
			}
		}
	}
	if base, _, _ := loadObjectBase(cfg); base != nil {
		if base.Rows != nil {
			if _, ok := base.Rows[id]; ok {
				return true
			}
		}
	}
	return false
}

func splitFourCCs(v string) []string {
	var out []string
	for _, tok := range strings.Split(v, ",") {
		tok = strings.TrimSpace(tok)
		if len(tok) == 4 && tok != "_" && tok != "\x00\x00\x00\x00" {
			out = append(out, tok)
		}
	}
	return out
}

// filePathCandidates returns the path forms to probe in the source archive for
// an icon/model field value (slash variants + a .blp fallback for extensionless
// icon stems).
func filePathCandidates(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" || v == "_" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(v)
	add(strings.ReplaceAll(v, "/", "\\"))
	add(strings.ReplaceAll(v, "\\", "/"))
	if !strings.Contains(filepath.Base(strings.ReplaceAll(v, "\\", "/")), ".") {
		add(v + ".blp")
		add(strings.ReplaceAll(v, "/", "\\") + ".blp")
	}
	return out
}

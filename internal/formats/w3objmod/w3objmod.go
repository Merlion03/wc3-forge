// Package w3objmod parses the WC3 object-modification table files:
// war3map.w3b (destructibles), war3map.w3d (doodads), war3map.w3u (units),
// war3map.w3t (items), war3map.w3a (abilities), and the war3mapSkin.*
// variants. These hold per-map overrides + new derived types ("Custom"
// objects) on top of the stock SLK tables.
//
// All five files share the same on-wire shape. Two flags differ:
//   - optional_ints: w3d + w3a use the optional level/variation + dataPointer
//     fields per modification; w3b/w3u/w3t do not.
//
// File layout:
//
//   uint32 version            (1, 2, or 3 — modern Reforged is 3)
//   // Original-table:
//   uint32 object_count
//   for object_count:
//     [4 bytes] original_id   (FourCC of stock base type, e.g. "ATtr")
//     [4 bytes] modified_id   (all-zero for this table)
//     if version >= 3:
//       uint32 set_count      (always 1 in practice)
//       uint32 set_flag
//     uint32 modification_count
//     for modification_count:
//       [4 bytes] modification_id  (field FourCC, e.g. "dfil" for "file")
//       uint32 type           (0=int, 1=float, 2=unreal, 3=string)
//       if optional_ints:
//         uint32 level_variation
//         uint32 data_pointer
//       data                  (per type)
//       uint32 end_marker     (zero terminator / padding)
//   // Custom-table: same shape, but original_id is the BASE type the new
//   // derived type inherits from, and modified_id is the new FourCC.
//   uint32 object_count
//   for object_count: (same as above)
//
// We extract only what the renderer needs:
//   - Custom objects: (newId, baseId, columnOverrides map[colName]value)
//   - Original-table edits (rare in custom maps) keyed by the original
//     FourCC, holding override columns.
package w3objmod

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Overrides is the per-row column → value map (lowercase column names,
// matching the SLK Mapped convention).
type Overrides map[string]string

// CustomObject is a new derived type defined in the map.
type CustomObject struct {
	ID        string    // 4-char FourCC of the new object (e.g. "D006")
	BaseID    string    // FourCC of the stock type it inherits from
	Overrides Overrides // explicitly-overridden columns
}

// OriginalEdit is an edit applied to a stock type's row.
type OriginalEdit struct {
	BaseID    string    // FourCC of the stock type being edited
	Overrides Overrides
}

// File is the parsed object-modification table.
type File struct {
	Version       uint32
	OriginalEdits []OriginalEdit
	Customs       []CustomObject
}

// FieldMap maps the 4-byte field FourCC (e.g. "dfil") to the column name
// (e.g. "file"). Built from {Doodad,Destructable,Unit,Item,Ability}MetaData.slk
// via row.field. The renderer needs this so the override-extraction code
// can produce column-keyed Overrides matching the base SLK schema.
type FieldMap map[string]string

// Parse reads a war3map.w3{bdutia} file. opt is true for w3d (doodads) +
// w3a (abilities), false for w3b/w3u/w3t. fields supplies the meta-data
// FourCC → column-name mapping; pass nil to keep modification IDs as
// columns directly (useful for debugging).
func Parse(data []byte, opt bool, fields FieldMap) (*File, error) {
	r := &reader{buf: data}
	out := &File{}
	if err := r.u32(&out.Version); err != nil {
		return nil, fmt.Errorf("version: %w", err)
	}
	if out.Version != 1 && out.Version != 2 && out.Version != 3 {
		return nil, fmt.Errorf("unknown version %d", out.Version)
	}

	if err := readTable(r, out, opt, fields, false); err != nil {
		return nil, fmt.Errorf("original table: %w", err)
	}
	if err := readTable(r, out, opt, fields, true); err != nil {
		return nil, fmt.Errorf("custom table: %w", err)
	}
	return out, nil
}

func readTable(r *reader, out *File, opt bool, fields FieldMap, custom bool) error {
	var objects uint32
	if err := r.u32(&objects); err != nil {
		return err
	}
	for i := uint32(0); i < objects; i++ {
		orig, err := r.fourCC()
		if err != nil {
			return fmt.Errorf("obj[%d].original_id: %w", i, err)
		}
		mod, err := r.fourCC()
		if err != nil {
			return fmt.Errorf("obj[%d].modified_id: %w", i, err)
		}
		if out.Version >= 3 {
			var setCount, setFlag uint32
			if err := r.u32(&setCount); err != nil {
				return err
			}
			if err := r.u32(&setFlag); err != nil {
				return err
			}
			_ = setCount
			_ = setFlag
		}
		var modCount uint32
		if err := r.u32(&modCount); err != nil {
			return err
		}
		ov := Overrides{}
		for j := uint32(0); j < modCount; j++ {
			modID, err := r.fourCC()
			if err != nil {
				return fmt.Errorf("obj[%d].mod[%d].id: %w", i, j, err)
			}
			var typ uint32
			if err := r.u32(&typ); err != nil {
				return err
			}
			if opt {
				var lv, dp uint32
				if err := r.u32(&lv); err != nil {
					return err
				}
				if err := r.u32(&dp); err != nil {
					return err
				}
				_ = lv
				_ = dp
			}
			val, err := readValue(r, typ)
			if err != nil {
				return fmt.Errorf("obj[%d].mod[%d].value: %w", i, j, err)
			}
			// End marker: 4 bytes (usually zero).
			var end uint32
			if err := r.u32(&end); err != nil {
				return err
			}
			col := modID
			if fields != nil {
				if c, ok := fields[modID]; ok {
					col = c
				}
			}
			ov[col] = val
		}
		if custom {
			out.Customs = append(out.Customs, CustomObject{
				ID: mod, BaseID: orig, Overrides: ov,
			})
		} else {
			out.OriginalEdits = append(out.OriginalEdits, OriginalEdit{
				BaseID: orig, Overrides: ov,
			})
		}
	}
	return nil
}

func readValue(r *reader, typ uint32) (string, error) {
	switch typ {
	case 0:
		var v int32
		if err := r.i32(&v); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d", v), nil
	case 1, 2:
		// type 1 = float, type 2 = "unreal" (fixed-point; treat as float)
		var bits uint32
		if err := r.u32(&bits); err != nil {
			return "", err
		}
		return fmt.Sprintf("%g", math.Float32frombits(bits)), nil
	case 3:
		s, err := r.cString()
		if err != nil {
			return "", err
		}
		return s, nil
	default:
		return "", fmt.Errorf("unknown value type %d", typ)
	}
}

// --- minimal reader helpers (no third-party deps) ---

type reader struct {
	buf []byte
	off int
}

func (r *reader) need(n int) error {
	if r.off+n > len(r.buf) {
		return fmt.Errorf("short read (have %d, need %d)", len(r.buf)-r.off, n)
	}
	return nil
}
func (r *reader) u32(out *uint32) error {
	if err := r.need(4); err != nil {
		return err
	}
	*out = binary.LittleEndian.Uint32(r.buf[r.off:])
	r.off += 4
	return nil
}
func (r *reader) i32(out *int32) error {
	if err := r.need(4); err != nil {
		return err
	}
	*out = int32(binary.LittleEndian.Uint32(r.buf[r.off:]))
	r.off += 4
	return nil
}
func (r *reader) fourCC() (string, error) {
	if err := r.need(4); err != nil {
		return "", err
	}
	s := string(r.buf[r.off : r.off+4])
	r.off += 4
	return s, nil
}
func (r *reader) cString() (string, error) {
	end := r.off
	for end < len(r.buf) && r.buf[end] != 0 {
		end++
	}
	if end >= len(r.buf) {
		return "", fmt.Errorf("unterminated string")
	}
	s := string(r.buf[r.off:end])
	r.off = end + 1
	return s, nil
}

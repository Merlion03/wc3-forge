// Encode is the byte-faithful inverse of Parse. It mirrors readEntity
// field-for-field so that Parse → Encode reproduces the original .doo bytes
// exactly for any well-formed input.
//
// Round-trip relies on Entity's unexported preservation fields
// (scaleRaw, skinIDPresent, itemDropSetSizes) — see the Entity struct
// definition for the rationale.

package unitsdoo

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Encode serializes f back to the on-disk war3mapUnits.doo binary format.
// For a file produced by Parse, Encode(Parse(data)) returns bytes equal to
// data. For a hand-constructed File, Encode emits a valid file that will
// round-trip through Parse — though Encode-then-Parse-then-Encode is the
// guaranteed-stable equivalence (the first Parse populates the unexported
// preservation fields).
func Encode(f *File) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("encode: nil file")
	}

	w := &writer{}

	// Header: "W3do" magic + version + subversion + entity count.
	if f.Format != [4]byte{} && string(f.Format[:]) != "W3do" {
		return nil, fmt.Errorf("encode: invalid magic %q, want \"W3do\"", string(f.Format[:]))
	}
	w.writeBytes([]byte("W3do"))
	w.writeU32(f.Version)
	w.writeU32(f.SubVersion)
	w.writeU32(uint32(len(f.Entities)))

	for i, e := range f.Entities {
		if err := writeEntity(w, &e, f.SubVersion); err != nil {
			return nil, fmt.Errorf("entity %d: %w", i, err)
		}
	}

	return w.bytes(), nil
}

func writeEntity(w *writer, e *Entity, subversion uint32) error {
	// type_id: 4 bytes, exactly.
	if len(e.TypeID) != 4 {
		return fmt.Errorf("type_id %q must be exactly 4 bytes", e.TypeID)
	}
	w.writeBytes([]byte(e.TypeID))

	w.writeU32(e.Variation)

	for i := 0; i < 3; i++ {
		w.writeF32(e.Position[i])
	}
	w.writeF32(e.Rotation)

	// Scale: prefer scaleRaw when Parse populated it. Otherwise derive from
	// Scale * 128 (HiveWE storage convention) — covers hand-constructed
	// entities. A zero scaleRaw means "not set"; this is safe because real
	// .doo entities never carry scale=0 (it would render the unit invisible).
	for i := 0; i < 3; i++ {
		if e.scaleRaw[i] != 0 {
			w.writeF32(e.scaleRaw[i])
		} else {
			w.writeF32(e.Scale[i] * 128.0)
		}
	}

	// skin_id: present whenever the original on-disk record carried it.
	//
	// skin_id presence is NOT a reliable function of the subversion (a
	// subversion-11 map may omit it entirely — e.g. Green Circle TD — and a
	// subversion-9 Reforged re-save may include it). Parse therefore resolves
	// presence per-file by trial and records the result in skinIDPresent.
	//
	//   - PARSED entities (e.parsed): trust skinIDPresent verbatim so the
	//     round-trip is byte-faithful regardless of subversion.
	//   - HAND-CONSTRUCTED entities (CreateUnit etc., e.parsed == false):
	//     skinIDPresent is meaningless, so fall back to the subversion rule
	//     (>= 11 emits a chunk, < 11 omits it). CreateUnit defaults SkinID to
	//     the TypeID, which keeps subversion-11 saves valid.
	var emitSkinID bool
	if e.parsed {
		emitSkinID = e.skinIDPresent
	} else {
		emitSkinID = subversion >= 11
	}
	if emitSkinID {
		if len(e.SkinID) != 4 {
			return fmt.Errorf("skin_id %q must be exactly 4 bytes when emitted", e.SkinID)
		}
		w.writeBytes([]byte(e.SkinID))
	}

	w.writeU8(e.Flags)
	w.writeU32(e.Player)
	w.writeU8(e.UnknownByte1)
	w.writeU8(e.UnknownByte2)

	w.writeU32(uint32(e.HitPointsPct))
	w.writeU32(uint32(e.ManaPct))

	// MapItemTable is subversion >= 11 only.
	if subversion >= 11 {
		w.writeU32(uint32(e.MapItemTable))
	}

	// Item drop sets: write per-set counts followed by the items in that set,
	// reconstructed from the flattened ItemDrops + itemDropSetSizes captured
	// by Parse. For hand-constructed entities without itemDropSetSizes, emit
	// a single set containing all drops (when any) — a defensible default.
	sizes := e.itemDropSetSizes
	if len(sizes) == 0 && len(e.ItemDrops) > 0 {
		sizes = []uint32{uint32(len(e.ItemDrops))}
	}
	// Validate sizes sum to len(ItemDrops) to catch misuse before producing
	// a file that won't round-trip.
	var sum uint32
	for _, n := range sizes {
		sum += n
	}
	if sum != uint32(len(e.ItemDrops)) {
		return fmt.Errorf("item_drop_set_sizes sum %d != ItemDrops len %d", sum, len(e.ItemDrops))
	}
	w.writeU32(uint32(len(sizes)))
	idx := 0
	for s, n := range sizes {
		w.writeU32(n)
		for i := uint32(0); i < n; i++ {
			d := e.ItemDrops[idx]
			if len(d.ItemID) != 4 {
				return fmt.Errorf("drop_set[%d].item[%d].id %q must be exactly 4 bytes", s, i, d.ItemID)
			}
			w.writeBytes([]byte(d.ItemID))
			w.writeU32(d.Chance)
			idx++
		}
	}

	w.writeU32(e.GoldAmount)
	w.writeF32(e.TargetAcquisition)
	w.writeU32(e.HeroLevel)
	if subversion >= 11 {
		w.writeU32(e.HeroStr)
		w.writeU32(e.HeroAgi)
		w.writeU32(e.HeroInt)
	}

	// Inventory.
	w.writeU32(uint32(len(e.Inventory)))
	for i, slot := range e.Inventory {
		if len(slot.ItemID) != 4 {
			return fmt.Errorf("inventory[%d].item_id %q must be exactly 4 bytes", i, slot.ItemID)
		}
		w.writeU32(slot.Slot)
		w.writeBytes([]byte(slot.ItemID))
	}

	// Ability modifications.
	w.writeU32(uint32(len(e.AbilityModifications)))
	for i, m := range e.AbilityModifications {
		if len(m.AbilityID) != 4 {
			return fmt.Errorf("ability[%d].id %q must be exactly 4 bytes", i, m.AbilityID)
		}
		w.writeBytes([]byte(m.AbilityID))
		var ac uint32
		if m.Autocast {
			ac = 1
		}
		w.writeU32(ac)
		w.writeU32(m.Level)
	}

	// Random type + variable payload.
	w.writeU32(e.RandomType)
	switch e.RandomType {
	case 0:
		if len(e.RandomData) != 4 {
			return fmt.Errorf("random_data (type=0) must be 4 bytes, got %d", len(e.RandomData))
		}
		w.writeBytes(e.RandomData)
	case 1:
		if len(e.RandomData) != 8 {
			return fmt.Errorf("random_data (type=1) must be 8 bytes, got %d", len(e.RandomData))
		}
		w.writeBytes(e.RandomData)
	case 2:
		// Variable: uint32 count, then count * 8 bytes.
		if len(e.RandomData)%8 != 0 {
			return fmt.Errorf("random_data (type=2) must be multiple of 8 bytes, got %d", len(e.RandomData))
		}
		w.writeU32(uint32(len(e.RandomData) / 8))
		w.writeBytes(e.RandomData)
	default:
		return fmt.Errorf("unknown random_type=%d", e.RandomType)
	}

	w.writeU32(uint32(e.CustomColor))
	w.writeU32(uint32(e.WaygateRegion))
	w.writeU32(e.CreationNumber)
	return nil
}

// --- writer ----------------------------------------------------------------

// writer is a minimal little-endian byte accumulator mirroring reader's surface.
type writer struct {
	buf []byte
}

func (w *writer) bytes() []byte { return w.buf }

func (w *writer) writeBytes(b []byte) { w.buf = append(w.buf, b...) }

func (w *writer) writeU8(v uint8) { w.buf = append(w.buf, v) }

func (w *writer) writeU32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	w.buf = append(w.buf, b[:]...)
}

func (w *writer) writeF32(v float32) {
	w.writeU32(math.Float32bits(v))
}

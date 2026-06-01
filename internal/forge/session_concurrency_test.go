package forge

import (
	"sync"
	"testing"

	"github.com/StephenSHorton/wc3-forge/internal/formats/unitsdoo"
)

// TestSaveConcurrentMutation_NoRace exercises the encode-under-RLock fix: a
// goroutine repeatedly Save()s a folder-backed map while other goroutines
// concurrently MoveUnit. Before the fix, Save's Encode read shared slices
// UNLOCKED while the mutators wrote them in place, tearing the output (and
// tripping the race detector). With the fix, Encode holds the read lock so the
// mutators (write lock) block during serialization.
//
// Run with `go test -race` for the definitive check — the detector flags the
// unsynchronized read/write the fix removes. Without -race this still asserts
// no Save error and that the final saved war3mapUnits.doo re-parses cleanly
// (a torn encode would corrupt the entity count or trail).
func TestSaveConcurrentMutation_NoRace(t *testing.T) {
	tmp := copyFixtureToTemp(t, `C:\Users\4step\projects\wc3-survival-game\map\extracted`)
	s := &Session{}
	if err := s.Open(tmp); err != nil {
		t.Fatalf("Open: %v", err)
	}
	units := s.Units()
	if units == nil || len(units.Entities) < 4 {
		t.Skipf("fixture has %d entities, test wants ≥4", len(units.Entities))
	}
	cns := make([]uint32, 0, 4)
	for i := 0; i < 4; i++ {
		cns = append(cns, units.Entities[i].CreationNumber)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Saver: repeatedly flush to the folder while mutations are in flight.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.Save(); err != nil {
				t.Errorf("concurrent Save: %v", err)
				return
			}
		}
	}()

	// Mutators: each hammers a distinct unit's position.
	for _, cn := range cns {
		wg.Add(1)
		go func(cn uint32) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = s.MoveUnit(cn, float32(i), float32(-i), 0)
			}
		}(cn)
	}

	wg.Wait()
	close(stop)

	// Final consistency check: a clean Save then a re-parse of the units file
	// must succeed (a torn concurrent encode would have produced a buffer whose
	// declared entity count or trailing bytes don't line up).
	if err := s.Save(); err != nil {
		t.Fatalf("final Save: %v", err)
	}
	src := s.source
	if src == nil {
		t.Fatal("no source after save")
	}
	data, ok, err := src.read("war3mapUnits.doo")
	if err != nil || !ok {
		t.Fatalf("read war3mapUnits.doo: ok=%v err=%v", ok, err)
	}
	if _, err := unitsdoo.Parse(data); err != nil {
		t.Errorf("saved war3mapUnits.doo failed to re-parse (torn encode?): %v", err)
	}
}

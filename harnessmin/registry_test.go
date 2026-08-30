package harnessmin_test

// The second reproduction's active path, mirrored.
//
// Its dump names TestRegisteredMigrationProvider_ConcurrentRegisterAndMigrations
// and sortMigrations. Reading that provider, the synchronisation is correct --
// both entry points hold the mutex and the getter returns slices.Clone -- so
// the test is not a race. What it is, is a dense concurrent allocator: four
// goroutines appending pointers to a growing slice while four more clone and
// walk it, a hundred times each.
//
// Together with the file loader, that gives this arm both paths the collector
// aborted under, and neither needs anything outside the standard library.

import (
	"slices"
	"sort"
	"sync"
)

type entry struct {
	version     int64
	description string
	up          func() error
	down        func() error
}

type registry struct {
	mu      sync.Mutex
	entries []*entry
	sorted  bool
}

func (r *registry) register(e *entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	r.sorted = false
}

func (r *registry) all() []*entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.sorted {
		sort.Slice(r.entries, func(i, j int) bool { return r.entries[i].version < r.entries[j].version })
		r.sorted = true
	}
	return slices.Clone(r.entries)
}

// churnRegistry runs the producers and consumers the reproducing test runs.
func churnRegistry(workers, iterations int) int {
	r := &registry{}
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range iterations {
				v := int64(w*iterations + i + 1)
				r.register(&entry{
					version:     v,
					description: "Concurrent migration",
					up:          func() error { return nil },
					down:        func() error { return nil },
				})
			}
		}(w)
	}
	seen := 0
	var mu sync.Mutex
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				got := r.all()
				mu.Lock()
				seen += len(got)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return seen
}

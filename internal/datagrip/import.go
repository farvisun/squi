package datagrip

import (
	"fmt"
	"strings"

	"github.com/farvisun/squi/internal/config"
)

// Options configure Import.
type Options struct {
	Root     string   // JetBrains configuration root (default Root())
	Projects []string // explicit sources (see SourceAt); default Discover
	Prune    bool     // remove profiles whose data source was deleted
	DryRun   bool     // report without saving
}

// Import syncs the saved profiles of store with DataGrip and returns what
// changed, plus the IDs of removed profiles (whose secrets the caller
// should delete).
func Import(store *config.Store, o Options) (Report, []string, error) {
	if o.Root == "" {
		o.Root = Root()
	}
	var sources []Source
	if len(o.Projects) == 0 {
		sources = Discover(o.Root)
		if len(sources) == 0 {
			return Report{}, nil, fmt.Errorf("no DataGrip data sources found under %s (use --project DIR)", o.Root)
		}
	}
	for _, p := range o.Projects {
		s, err := SourceAt(p, o.Root)
		if err != nil {
			return Report{}, nil, err
		}
		sources = append(sources, s)
	}
	conns, err := store.LoadConnections()
	if err != nil {
		return Report{}, nil, err
	}
	items, rep := Read(sources)
	out, rep := Sync(conns, items, rep, o.Prune)
	if o.DryRun {
		return rep, nil, nil
	}
	var removed []string
	kept := map[string]bool{}
	for _, c := range out {
		kept[c.ID] = true
	}
	for _, c := range conns {
		if !kept[c.ID] {
			removed = append(removed, c.ID)
		}
	}
	if rep.Changed() {
		if err := store.SaveConnections(out); err != nil {
			return rep, nil, err
		}
	}
	return rep, removed, nil
}

// Summary is a one-line count of the changes.
func (r Report) Summary() string {
	parts := []string{fmt.Sprintf("%d new", len(r.Added)), fmt.Sprintf("%d updated", len(r.Updated)),
		fmt.Sprintf("%d unchanged", len(r.Unchanged))}
	if len(r.Removed) > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", len(r.Removed)))
	}
	if len(r.Stale) > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted in DataGrip", len(r.Stale)))
	}
	if len(r.Skipped) > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", len(r.Skipped)))
	}
	return strings.Join(parts, ", ")
}

// Details lists the changes, skipped data sources and notes, one per line.
func (r Report) Details() string {
	var b strings.Builder
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", title)
		for _, l := range lines {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	section("New", r.Added)
	section("Updated", r.Updated)
	section("Removed", r.Removed)
	section("Deleted in DataGrip (kept; --prune removes them)", r.Stale)
	section("Skipped", r.Skipped)
	section("Notes", r.Notes)
	return strings.TrimRight(b.String(), "\n")
}

package datagrip

import (
	"reflect"
	"strconv"
	"strings"
	"unicode"

	"github.com/farvisun/squi/internal/config"
)

// Item is a data source converted to a profile.
type Item struct {
	Conn  config.Connection
	Notes []string
}

// Report describes an import.
type Report struct {
	Added, Updated, Unchanged []string // profile names
	Removed                   []string // imported earlier, gone from DataGrip, pruned
	Stale                     []string // imported earlier, gone from DataGrip, kept
	Skipped                   []string // "name: reason"
	Notes                     []string // "name: note"
	Sources                   []string // the directories read
}

// Changed reports whether the import changes the saved profiles.
func (r Report) Changed() bool { return len(r.Added)+len(r.Updated)+len(r.Removed) > 0 }

// Read loads and converts the data sources of every source. Sources that
// cannot be read and data sources squi cannot use are listed in the
// report's Skipped.
func Read(sources []Source) ([]Item, Report) {
	var items []Item
	var rep Report
	for _, src := range sources {
		rep.Sources = append(rep.Sources, src.Key())
		dss, err := Load(src)
		if err != nil {
			rep.Skipped = append(rep.Skipped, src.Dir+": "+err.Error())
			continue
		}
		for _, ds := range dss {
			c, notes, err := Convert(ds, src)
			if err != nil {
				rep.Skipped = append(rep.Skipped, c.Name+": "+err.Error())
				continue
			}
			items = append(items, Item{Conn: c, Notes: notes})
		}
	}
	return items, rep
}

// Sync merges imported profiles into conns. A profile imported before
// (matched by the DataGrip id) gets DataGrip's current address, user,
// database, TLS, SSH and parameters, and keeps what is squi's own: its ID,
// environment, label, colour, timeout, fetch size and password settings.
// Read-only is kept once set on either side. New profiles get an
// environment guessed from their name (prod, staging, dev, local).
//
// Profiles imported from one of rep.Sources whose data source is gone are
// removed when prune is set and reported as stale otherwise. Profiles not
// imported from DataGrip are never changed.
func Sync(conns []config.Connection, items []Item, rep Report, prune bool) ([]config.Connection, Report) {
	out := append([]config.Connection(nil), conns...)
	index := map[string]int{}
	for i, c := range out {
		if imported(c) {
			index[c.Origin.ID] = i
		}
	}
	seen := map[string]bool{}
	for _, it := range items {
		n := it.Conn
		id := n.Origin.ID
		if seen[id] {
			continue // the same data source in two projects: the first wins
		}
		seen[id] = true
		for _, note := range it.Notes {
			rep.Notes = append(rep.Notes, n.Name+": "+note)
		}
		if i, ok := index[id]; ok {
			old := out[i]
			m := old
			m.Name = uniqueName(out, i, n.Name)
			m.Driver, m.Host, m.Port, m.User, m.Database = n.Driver, n.Host, n.Port, n.User, n.Database
			m.SSLMode, m.SSLRootCert, m.SSLCert, m.SSLKey = n.SSLMode, n.SSLRootCert, n.SSLCert, n.SSLKey
			m.SSH, m.Params, m.Origin = n.SSH, n.Params, n.Origin
			m.ReadOnly = old.ReadOnly || n.ReadOnly
			if reflect.DeepEqual(m, old) {
				rep.Unchanged = append(rep.Unchanged, m.Name)
			} else {
				out[i] = m
				rep.Updated = append(rep.Updated, m.Name)
			}
			continue
		}
		n.ID = config.NewID()
		n.Name = uniqueName(out, -1, n.Name)
		n.Env = GuessEnv(n)
		out = append(out, n)
		rep.Added = append(rep.Added, n.Name)
	}
	scanned := map[string]bool{}
	for _, s := range rep.Sources {
		scanned[s] = true
	}
	kept := out[:0]
	for _, c := range out {
		if imported(c) && !seen[c.Origin.ID] && scanned[c.Origin.Source] {
			if prune {
				rep.Removed = append(rep.Removed, c.Name)
				continue
			}
			rep.Stale = append(rep.Stale, c.Name)
		}
		kept = append(kept, c)
	}
	return kept, rep
}

func imported(c config.Connection) bool { return c.Origin != nil && c.Origin.Tool == Tool }

// uniqueName returns name, suffixed when another profile (other than the
// one at self) already uses it.
func uniqueName(conns []config.Connection, self int, name string) string {
	taken := func(n string) bool {
		for i, c := range conns {
			if i != self && strings.EqualFold(c.Name, n) {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	cand := name + " (DataGrip)"
	for k := 2; taken(cand); k++ {
		cand = name + " (DataGrip " + strconv.Itoa(k) + ")"
	}
	return cand
}

// GuessEnv infers an environment from the words of a profile's name, or
// local for SQLite files and unforwarded localhost addresses.
func GuessEnv(c config.Connection) config.Env {
	words := strings.FieldsFunc(strings.ToLower(c.Name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	has := func(set ...string) bool {
		for _, w := range words {
			for _, s := range set {
				if w == s {
					return true
				}
			}
		}
		return false
	}
	switch {
	case has("prod", "production", "prd", "live"):
		return config.EnvProd
	case has("staging", "stage", "stg", "uat", "preprod"):
		return config.EnvStaging
	case has("dev", "develop", "development", "test", "qa"):
		return config.EnvDev
	case has("local", "localhost"):
		return config.EnvLocal
	case c.Driver == "sqlite":
		return config.EnvLocal
	case !c.SSH.Enabled && (c.Host == "localhost" || c.Host == "127.0.0.1" || c.Host == "::1"):
		return config.EnvLocal
	}
	return config.EnvNone
}

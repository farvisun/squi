package ui

import (
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/config"
)

func explorerNode(e *explorer, label string) int {
	for i, n := range e.flat {
		if n.label == label {
			return i
		}
	}
	return -1
}

func TestPinTableInExplorer(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	e := tb.ac.explorer
	m.focus = focusExplorer
	ix := explorerNode(e, "users")
	if ix < 0 {
		t.Fatalf("users not in explorer: %d nodes", len(e.flat))
	}
	e.ix = ix
	key(m, "*")
	if e.roots[0].kind != nFavorites || len(e.roots[0].children) != 1 || e.roots[0].children[0].label != "public.users" {
		t.Fatalf("favorites group: %+v", e.roots[0])
	}
	saved, _ := m.store.LoadFavorites()
	if len(saved["c"]) != 1 || saved["c"][0].Name != "users" || saved["c"][0].Schema != "public" || saved["c"][0].Database != "app" {
		t.Fatalf("saved: %+v", saved)
	}
	if !strings.Contains(m.View(), "★ Favorites (1)") {
		t.Fatal("favorites not rendered")
	}
	e.ix = explorerNode(e, "public.users")
	key(m, "enter")
	if got := lastStatement(fc); got != `SELECT * FROM "public"."users"` {
		t.Fatalf("open favorite: %s", got)
	}
	m.focus = focusExplorer
	e.ix = explorerNode(e, "public.users")
	key(m, "*") // unpin
	if len(e.roots) > 0 && e.roots[0].kind == nFavorites {
		t.Fatal("favorites group should disappear")
	}
	if saved, _ := m.store.LoadFavorites(); len(saved["c"]) != 0 {
		t.Fatalf("not removed: %+v", saved)
	}
}

func TestBookmarkFilteredView(t *testing.T) {
	m, tb, fc := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	openBrowse(t, m, tb, "users")
	key(m, "/")
	submitPrompt(t, m, "email like '%@x.io'")
	key(m, "s") // order by id
	key(m, "*")
	saved, _ := m.store.LoadFavorites()
	if len(saved["c"]) != 1 {
		t.Fatalf("saved: %+v", saved)
	}
	f := saved["c"][0]
	if f.Where != "email like '%@x.io'" || f.OrderBy != "id" || f.Desc || !f.IsView() {
		t.Fatalf("bookmark: %+v", f)
	}
	// reopen from the explorer
	e := tb.ac.explorer
	m.focus = focusExplorer
	e.ix = -1
	for i, n := range e.flat {
		if n.fav != nil {
			e.ix = i
		}
	}
	if e.ix < 0 || !strings.Contains(e.flat[e.ix].label, "⟨email like '%@x.io' ↕id⟩") {
		t.Fatalf("bookmark node missing")
	}
	key(m, "enter")
	if got := lastStatement(fc); got != `SELECT * FROM "public"."users" WHERE (email like '%@x.io') ORDER BY "id"` {
		t.Fatalf("reopen: %s", got)
	}
	if r := tb.out.current().(*resultPanel); r.browse == nil || r.browse.Where == "" {
		t.Fatal("reopened bookmark should be a filtered browse panel")
	}
}

func TestFavoritesLoadedOnStart(t *testing.T) {
	m, tb, _ := newTestModel(t, config.Connection{ID: "c", Name: "dev", Driver: "postgres", Database: "app"})
	tb.ac.explorer.setFavorites([]config.Favorite{{Database: "app", Schema: "public", Name: "orders", Kind: "table"}})
	drain(m, tb.ac.explorer.load(nil)) // reloading the databases keeps the group on top
	if r := tb.ac.explorer.roots; len(r) < 2 || r[0].kind != nFavorites || r[1].kind != nDatabase || !r[1].expanded {
		t.Fatalf("roots after reload: %d", len(r))
	}
}

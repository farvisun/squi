package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Favorite is a table (or other object) pinned in the explorer. With Where
// or OrderBy set it is a bookmarked view of the table's rows.
type Favorite struct {
	Database string `json:"database,omitempty"`
	Schema   string `json:"schema,omitempty"`
	Name     string `json:"name"`
	Kind     string `json:"kind,omitempty"`
	Where    string `json:"where,omitempty"`
	OrderBy  string `json:"order_by,omitempty"`
	Desc     bool   `json:"desc,omitempty"`
}

// SameObject reports whether f and g pin the same object (ignoring the view).
func (f Favorite) SameObject(g Favorite) bool {
	return f.Database == g.Database && f.Schema == g.Schema && f.Name == g.Name
}

// IsView reports whether f bookmarks a filtered or sorted view.
func (f Favorite) IsView() bool { return f.Where != "" || f.OrderBy != "" }

// Favorites maps a connection ID to its pinned objects, in display order.
type Favorites map[string][]Favorite

// FavoritesPath is where pinned objects are stored.
func (s *Store) FavoritesPath() string { return filepath.Join(s.StateDir, "favorites.json") }

// LoadFavorites returns the pinned objects of every connection.
func (s *Store) LoadFavorites() (Favorites, error) {
	f := Favorites{}
	b, err := os.ReadFile(s.FavoritesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return Favorites{}, err
	}
	return f, nil
}

// SaveFavorites writes the pinned objects (mode 0600: WHERE text may hold data).
func (s *Store) SaveFavorites(f Favorites) error {
	for id, list := range f {
		if len(list) == 0 {
			delete(f, id)
		}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(s.FavoritesPath(), b)
}

// Toggle adds fav to the connection's list, or removes it when an identical
// entry exists. It reports whether fav is now present.
func (f Favorites) Toggle(connID string, fav Favorite) bool {
	list := f[connID]
	for i, x := range list {
		if x == fav {
			f[connID] = append(list[:i:i], list[i+1:]...)
			return false
		}
	}
	f[connID] = append(list, fav)
	return true
}

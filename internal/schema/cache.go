// Package schema caches database metadata per connection and provides
// context-aware SQL autocompletion on top of it.
package schema

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/farvisun/squi/internal/db"
)

// Cache holds the databases, schemas, objects and columns of one connection.
// It is safe for concurrent use. Lookups match names exactly first and then
// case-insensitively. Returned slices must not be modified.
type Cache struct {
	mu        sync.RWMutex
	driver    db.DriverName
	databases []string
	dbs       map[string]*dbMeta
}

type dbMeta struct {
	schemas []string
	objects map[string][]db.Object         // schema -> objects
	columns map[string]map[string][]string // schema -> table -> columns
}

// NewCache returns an empty cache for an engine.
func NewCache(d db.DriverName) *Cache {
	return &Cache{driver: d, dbs: map[string]*dbMeta{}}
}

// Driver returns the engine the cache was created for.
func (c *Cache) Driver() db.DriverName { return c.driver }

// SetDatabases records the server's database names.
func (c *Cache) SetDatabases(names []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.databases = slices.Clone(names)
}

// Databases returns the recorded database names.
func (c *Cache) Databases() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.databases
}

// meta returns (creating) the entry of database; c.mu must be held for writing.
func (c *Cache) meta(database string) *dbMeta {
	m := c.dbs[database]
	if m == nil {
		m = &dbMeta{objects: map[string][]db.Object{}, columns: map[string]map[string][]string{}}
		c.dbs[database] = m
	}
	return m
}

// lookup finds database's entry; c.mu must be held.
func (c *Cache) lookup(database string) *dbMeta {
	if m, ok := c.dbs[database]; ok {
		return m
	}
	return c.dbs[foldKey(c.dbs, database)]
}

// foldKey returns the key of m equal to k ignoring case, or k.
func foldKey[V any](m map[string]V, k string) string {
	for key := range m {
		if strings.EqualFold(key, k) {
			return key
		}
	}
	return k
}

func get[V any](m map[string]V, k string) V {
	if v, ok := m[k]; ok {
		return v
	}
	return m[foldKey(m, k)]
}

// SetSchemas records the schemas of a database.
func (c *Cache) SetSchemas(database string, schemas []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.meta(database).schemas = slices.Clone(schemas)
}

// Schemas returns the recorded schemas of a database.
func (c *Cache) Schemas(database string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m := c.lookup(database); m != nil {
		return m.schemas
	}
	return nil
}

// SetObjects records the objects of a schema.
func (c *Cache) SetObjects(database, schema string, objs []db.Object) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.meta(database).objects[schema] = slices.Clone(objs)
}

// SetColumns records table/view name -> column names for a schema.
func (c *Cache) SetColumns(database, schema string, cols map[string][]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make(map[string][]string, len(cols))
	for k, v := range cols {
		cp[k] = slices.Clone(v)
	}
	c.meta(database).columns[schema] = cp
}

// Objects returns the recorded objects of a schema.
func (c *Cache) Objects(database, schema string) []db.Object {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m := c.lookup(database); m != nil {
		return get(m.objects, schema)
	}
	return nil
}

// Columns returns the column names of a table or view.
func (c *Cache) Columns(database, schema, table string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if m := c.lookup(database); m != nil {
		return get(get(m.columns, schema), table)
	}
	return nil
}

// Invalidate drops the metadata of database, or everything if database is "".
func (c *Cache) Invalidate(database string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if database == "" {
		c.databases = nil
		c.dbs = map[string]*dbMeta{}
		return
	}
	delete(c.dbs, database)
}

// IsSystemSchema reports whether a Postgres schema holds catalog objects.
func IsSystemSchema(s string) bool {
	return s == "pg_catalog" || s == "information_schema" || s == "pg_toast" ||
		strings.HasPrefix(s, "pg_temp_") || strings.HasPrefix(s, "pg_toast_temp_")
}

// Load fetches the schemas, objects and columns of database via conn. For
// Postgres every non-system schema is loaded; for MySQL the database itself.
// Failures on individual schemas are collected into the returned error
// without aborting the rest.
func (c *Cache) Load(ctx context.Context, conn db.Conn, database string) error {
	schemas := []string{database}
	if !c.driver.MySQLStyle() {
		var err error
		if schemas, err = conn.Schemas(ctx, database); err != nil {
			return err
		}
	}
	c.SetSchemas(database, schemas)
	var errs []error
	for _, s := range schemas {
		if !c.driver.MySQLStyle() && IsSystemSchema(s) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		objs, err := conn.Objects(ctx, database, s)
		if err != nil {
			errs = append(errs, fmt.Errorf("schema %s: objects: %w", s, err))
			continue
		}
		c.SetObjects(database, s, objs)
		cols, err := conn.AllColumns(ctx, database, s)
		if err != nil {
			errs = append(errs, fmt.Errorf("schema %s: columns: %w", s, err))
			continue
		}
		c.SetColumns(database, s, cols)
	}
	return errors.Join(errs...)
}

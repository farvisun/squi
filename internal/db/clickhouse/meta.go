package clickhouse

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

func p(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// Databases lists databases with their on-disk size.
func (c *conn) Databases(ctx context.Context) ([]db.Database, error) {
	t, err := c.cl.table(ctx, `
SELECT d.name, if(s.bytes > 0, formatReadableSize(s.bytes), '')
FROM system.databases AS d
LEFT JOIN (SELECT database, sum(bytes_on_disk) AS bytes FROM system.parts WHERE active GROUP BY database) AS s
  ON s.database = d.name
ORDER BY d.name`, "", nil)
	if err != nil {
		return nil, err
	}
	out := make([]db.Database, len(t.Rows))
	for i, r := range t.Rows {
		out[i] = db.Database{Name: r[0].S, Size: r[1].S}
	}
	return out, nil
}

// Schemas returns the database itself: ClickHouse has no schema level.
func (c *conn) Schemas(_ context.Context, database string) ([]string, error) {
	return []string{database}, nil
}

// Objects lists tables, views, materialized views and dictionaries.
func (c *conn) Objects(ctx context.Context, database, _ string) ([]db.Object, error) {
	t, err := c.cl.table(ctx, `
SELECT name, engine, comment FROM system.tables
WHERE database = {db:String} AND NOT is_temporary AND NOT startsWith(name, '.inner')
ORDER BY name`, "", p("db", database))
	if err != nil {
		return nil, err
	}
	out := make([]db.Object, 0, len(t.Rows))
	for _, r := range t.Rows {
		out = append(out, db.Object{Schema: database, Name: r[0].S, Kind: kindOf(r[1].S), Comment: r[2].S})
	}
	return out, nil
}

func kindOf(engine string) db.ObjectKind {
	switch engine {
	case "View", "LiveView", "WindowView":
		return db.KindView
	case "MaterializedView":
		return db.KindMatView
	}
	return db.KindTable
}

// AllColumns maps table names to their columns for autocomplete.
func (c *conn) AllColumns(ctx context.Context, database, _ string) (map[string][]string, error) {
	t, err := c.cl.table(ctx, `
SELECT table, name FROM system.columns WHERE database = {db:String} ORDER BY table, position`, "", p("db", database))
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, r := range t.Rows {
		out[r[0].S] = append(out[r[0].S], r[1].S)
	}
	return out, nil
}

// TableDetail describes columns, keys, skipping indexes and storage stats.
func (c *conn) TableDetail(ctx context.Context, ref db.ObjectRef) (*db.TableDetail, error) {
	database := ref.Database
	if database == "" {
		database = ref.Schema
	}
	args := p("db", database, "t", ref.Name)
	info, err := c.cl.table(ctx, `
SELECT engine, engine_full, comment, partition_key, sorting_key, primary_key, sampling_key,
       total_rows, total_bytes, metadata_modification_time
FROM system.tables WHERE database = {db:String} AND name = {t:String}`, "", args)
	if err != nil {
		return nil, err
	}
	if len(info.Rows) == 0 {
		return nil, fmt.Errorf("table %s.%s not found", database, ref.Name)
	}
	ti := info.Rows[0]
	d := &db.TableDetail{Ref: ref, Comment: ti[2].S}

	cols, err := c.cl.table(ctx, `
SELECT position, name, type, default_kind, default_expression, comment,
       is_in_primary_key, is_in_sorting_key, is_in_partition_key, compression_codec
FROM system.columns WHERE database = {db:String} AND table = {t:String} ORDER BY position`, "", args)
	if err != nil {
		return nil, err
	}
	for _, r := range cols.Rows {
		pos, _ := strconv.Atoi(r[0].S)
		col := db.Column{Position: pos, Name: r[1].S, Type: r[2].S, Comment: r[5].S,
			Nullable: strings.HasPrefix(r[2].S, "Nullable("), IsPK: r[6].S == "1"}
		if r[3].S != "" {
			def := r[4].S
			if r[3].S != "DEFAULT" {
				def = r[3].S + " " + def // MATERIALIZED / ALIAS / EPHEMERAL
			}
			col.Default = &def
		}
		var extra []string
		if r[7].S == "1" && r[6].S != "1" {
			extra = append(extra, "sorting key")
		}
		if r[8].S == "1" {
			extra = append(extra, "partition key")
		}
		if r[9].S != "" {
			extra = append(extra, r[9].S)
		}
		col.Extra = strings.Join(extra, ", ")
		d.Columns = append(d.Columns, col)
	}

	if pk := ti[5].S; pk != "" {
		d.Indexes = append(d.Indexes, db.Index{Name: "PRIMARY KEY", Primary: true, Method: "sparse",
			Columns: splitKey(pk), Definition: "PRIMARY KEY (" + pk + ")"})
	}
	if sk := ti[4].S; sk != "" && sk != ti[5].S {
		d.Indexes = append(d.Indexes, db.Index{Name: "ORDER BY", Method: "sorting",
			Columns: splitKey(sk), Definition: "ORDER BY (" + sk + ")"})
	}
	if ix, err := c.cl.table(ctx, `
SELECT name, type_full, expr, granularity, formatReadableSize(data_compressed_bytes)
FROM system.data_skipping_indices WHERE database = {db:String} AND table = {t:String}`, "", args); err == nil {
		for _, r := range ix.Rows {
			d.Indexes = append(d.Indexes, db.Index{Name: r[0].S, Method: r[1].S, Columns: []string{r[2].S}, Size: r[4].S,
				Definition: fmt.Sprintf("INDEX %s %s TYPE %s GRANULARITY %s", r[0].S, r[2].S, r[1].S, r[3].S)})
		}
	}

	add := func(k, v string) {
		if v != "" && v != `\N` {
			d.Stats = append(d.Stats, db.KV{Key: k, Value: v})
		}
	}
	add("Engine", ti[0].S)
	add("Partition key", ti[3].S)
	add("Sorting key", ti[4].S)
	add("Primary key", ti[5].S)
	add("Sampling key", ti[6].S)
	add("Rows", ti[7].S)
	add("Modified", ti[9].S)
	if parts, err := c.cl.table(ctx, `
SELECT count(), uniqExact(partition), formatReadableSize(sum(bytes_on_disk)),
       formatReadableSize(sum(data_compressed_bytes)), formatReadableSize(sum(data_uncompressed_bytes)),
       if(sum(data_compressed_bytes) > 0, toString(round(sum(data_uncompressed_bytes) / sum(data_compressed_bytes), 2)), '')
FROM system.parts WHERE active AND database = {db:String} AND table = {t:String}`, "", args); err == nil && len(parts.Rows) > 0 && parts.Rows[0][0].S != "0" {
		r := parts.Rows[0]
		add("Parts", r[0].S)
		add("Partitions", r[1].S)
		add("Size on disk", r[2].S)
		add("Compressed", r[3].S)
		add("Uncompressed", r[4].S)
		add("Compression ratio", r[5].S)
	}
	if ef := ti[1].S; ef != "" && ef != ti[0].S {
		d.Constraints = append(d.Constraints, db.Constraint{Name: "engine", Type: "ENGINE", Definition: ef})
	}
	return d, nil
}

// splitKey splits "a, toDate(b), c" at top-level commas.
func splitKey(k string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range k {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(k[start:i]))
				start = i + 1
			}
		}
	}
	if s := strings.TrimSpace(k[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

// DDL returns the formatted CREATE statement.
func (c *conn) DDL(ctx context.Context, ref db.ObjectRef) (string, error) {
	if ref.Database == "" {
		ref.Database = ref.Schema
	}
	t, err := c.cl.table(ctx, "SHOW CREATE TABLE "+c.Dialect().Qualified(ref), "", nil)
	if err != nil {
		return "", err
	}
	if len(t.Rows) == 0 {
		return "", fmt.Errorf("no DDL for %s", ref.Name)
	}
	return strings.TrimSpace(t.Rows[0][0].S) + ";", nil
}

// Explain returns EXPLAIN PLAN (with index usage) as a tree. ClickHouse
// has no EXPLAIN ANALYZE.
func (s *session) Explain(ctx context.Context, sql string, analyze bool) (*db.PlanNode, error) {
	if analyze {
		return nil, fmt.Errorf("EXPLAIN ANALYZE: %w", db.ErrNotSupported)
	}
	sql = strings.TrimRight(strings.TrimSpace(sql), "; \t\r\n")
	t, err := s.c.cl.table(ctx, "EXPLAIN indexes = 1 "+sql, s.db, nil)
	if err != nil {
		return nil, err
	}
	lines := make([]string, len(t.Rows))
	for i, r := range t.Rows {
		lines[i] = r[0].S
	}
	return planTree(lines), nil
}

// planTree builds a tree from EXPLAIN output, nesting by indentation. A
// step's own details ("Indexes:", "Prewhere info:", …) are printed at the
// step's indentation, so they are hung below the step they follow.
func planTree(lines []string) *db.PlanNode {
	root := &db.PlanNode{Title: "Plan", Raw: strings.Join(lines, "\n")}
	type level struct {
		indent int // twice the column, so a step's details can sit at +1
		node   *db.PlanNode
	}
	stack := []level{{-1, root}}
	pop := func(indent int) {
		for len(stack) > 1 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
	}
	for _, l := range lines {
		text := strings.TrimLeft(l, " ")
		if text == "" {
			continue
		}
		indent := 2 * (len(l) - len(text))
		n := &db.PlanNode{Title: text}
		pop(indent + 1)
		if top := stack[len(stack)-1]; strings.Contains(text, ":") && top.indent == indent && !strings.Contains(top.node.Title, ":") {
			indent++
		} else {
			pop(indent)
		}
		parent := stack[len(stack)-1].node
		parent.Children = append(parent.Children, n)
		stack = append(stack, level{indent, n})
	}
	if len(root.Children) == 1 {
		only := root.Children[0]
		only.Raw = root.Raw
		return only
	}
	return root
}

// ---- admin ----

type admin struct{ c *conn }

func (a admin) Processes(ctx context.Context) (*db.Table, error) {
	return a.c.cl.table(ctx, `
SELECT query_id, user, address, round(elapsed, 1) AS elapsed_s, read_rows,
       formatReadableSize(memory_usage) AS memory, query
FROM system.processes ORDER BY elapsed DESC`, "", nil)
}

func (a admin) CancelQuery(ctx context.Context, id string) error {
	_, err := a.c.cl.exec(ctx, request{query: "KILL QUERY WHERE query_id = {id:String} ASYNC", params: p("id", id)})
	return err
}

// Kill is the same as CancelQuery: ClickHouse sessions have no server
// process to terminate.
func (a admin) Kill(ctx context.Context, id string) error { return a.CancelQuery(ctx, id) }

func (a admin) Variables(ctx context.Context) (*db.Table, error) {
	return a.c.cl.table(ctx, `SELECT name, value, changed, description FROM system.settings ORDER BY name`, "", nil)
}

func (a admin) Users(ctx context.Context) (*db.Table, error) {
	return a.c.cl.table(ctx, `SELECT name, storage, toString(auth_type) AS auth, default_database FROM system.users ORDER BY name`, "", nil)
}

func (a admin) Grants(ctx context.Context, user string) (*db.Table, error) {
	return a.c.cl.table(ctx, "SHOW GRANTS FOR "+a.c.Dialect().QuoteIdent(user), "", nil)
}

func (a admin) Sizes(ctx context.Context) (*db.Table, error) {
	return a.c.cl.table(ctx, `
SELECT database, table, sum(rows) AS rows, formatReadableSize(sum(bytes_on_disk)) AS on_disk,
       formatReadableSize(sum(data_uncompressed_bytes)) AS uncompressed, count() AS parts
FROM system.parts WHERE active
GROUP BY database, table ORDER BY sum(bytes_on_disk) DESC`, "", nil)
}

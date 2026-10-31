package schemadiff

import (
	"strings"
	"testing"

	"github.com/farvisun/squi/internal/db"
)

func str(s string) *string { return &s }

func schemas(d db.DriverName) (*Schema, *Schema) {
	src := &Schema{Driver: d, Database: "app", Schema: "public", Tables: map[string]*Table{
		"users": {Name: "users", Kind: db.KindTable, Detail: &db.TableDetail{
			Columns: []db.Column{
				{Name: "id", Type: "bigint", IsPK: true},
				{Name: "email", Type: "text"},
				{Name: "name", Type: "varchar(200)", Nullable: true},
				{Name: "active", Type: "boolean", Default: str("true")},
				{Name: "org_id", Type: "bigint", Nullable: true},
			},
			Indexes: []db.Index{
				{Name: "users_pkey", Columns: []string{"id"}, Primary: true, Unique: true},
				{Name: "users_email_key", Columns: []string{"email"}, Unique: true},
				{Name: "users_org_idx", Columns: []string{"org_id"}},
			},
			ForeignKeys: []db.ForeignKey{{Name: "users_org_fk", Columns: []string{"org_id"}, RefTable: "orgs", RefColumns: []string{"id"}, OnDelete: "CASCADE"}},
		}},
		"orgs":   {Name: "orgs", Kind: db.KindTable, Detail: &db.TableDetail{Columns: []db.Column{{Name: "id", Type: "bigint", IsPK: true}}}, DDL: "CREATE TABLE public.orgs (id bigint PRIMARY KEY);"},
		"active": {Name: "active", Kind: db.KindView, Detail: &db.TableDetail{}},
	}}
	dst := &Schema{Driver: d, Database: "app", Schema: "public", Tables: map[string]*Table{
		"users": {Name: "users", Kind: db.KindTable, Detail: &db.TableDetail{
			Columns: []db.Column{
				{Name: "id", Type: "bigint", IsPK: true},
				{Name: "email", Type: "varchar(100)", Nullable: true},
				{Name: "name", Type: "VARCHAR(200)", Nullable: true},
				{Name: "active", Type: "boolean"},
				{Name: "legacy", Type: "text", Nullable: true},
			},
			Indexes: []db.Index{
				{Name: "users_pkey", Columns: []string{"id"}, Primary: true, Unique: true},
				{Name: "users_email_key", Columns: []string{"email"}},
				{Name: "users_legacy_idx", Columns: []string{"legacy"}},
			},
		}},
		"active":  {Name: "active", Kind: db.KindView, Detail: &db.TableDetail{}},
		"old_log": {Name: "old_log", Kind: db.KindTable, Detail: &db.TableDetail{}},
	}}
	return src, dst
}

func TestDiff(t *testing.T) {
	src, dst := schemas(db.Postgres)
	var got []string
	for _, c := range Diff(src, dst) {
		got = append(got, string(c.Kind)+" "+c.Table+"."+c.Name)
	}
	want := []string{
		"missing table orgs.",
		"column type users.email",
		"nullability users.email",
		"default users.active",
		"missing column users.org_id",
		"extra column users.legacy",
		"index users.users_email_key",
		"missing index users.users_org_idx",
		"extra index users.users_legacy_idx",
		"missing foreign key users.users_org_fk",
		"extra table old_log.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("diff:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestScriptPostgres(t *testing.T) {
	src, dst := schemas(db.Postgres)
	script := Script(Diff(src, dst), src, dst, Options{})
	for _, want := range []string{
		"CREATE TABLE public.orgs (id bigint PRIMARY KEY);",
		`ALTER TABLE "public"."users" ALTER COLUMN "email" TYPE text;`,
		`ALTER TABLE "public"."users" ALTER COLUMN "email" SET NOT NULL;`,
		`ALTER TABLE "public"."users" ALTER COLUMN "active" SET DEFAULT true;`,
		`ALTER TABLE "public"."users" ADD COLUMN "org_id" bigint;`,
		`-- ALTER TABLE "public"."users" DROP COLUMN "legacy";  (review; run with -drop to include)`,
		`DROP INDEX "public"."users_email_key";`,
		`CREATE UNIQUE INDEX "users_email_key" ON "public"."users" ("email");`,
		`CREATE INDEX "users_org_idx" ON "public"."users" ("org_id");`,
		`-- DROP INDEX "public"."users_legacy_idx";`,
		`ALTER TABLE "public"."users" ADD CONSTRAINT "users_org_fk" FOREIGN KEY ("org_id") REFERENCES "public"."orgs" ("id") ON DELETE CASCADE;`,
		`-- DROP TABLE "public"."old_log";`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	if withDrops := Script(Diff(src, dst), src, dst, Options{Drops: true}); !strings.Contains(withDrops, "\nDROP TABLE \"public\".\"old_log\";") {
		t.Errorf("-drop should write the DROP:\n%s", withDrops)
	}
}

func TestScriptMySQLModifiesOnce(t *testing.T) {
	src, dst := schemas(db.MySQL)
	src.Schema, dst.Schema = "", ""
	script := Script(Diff(src, dst), src, dst, Options{})
	if n := strings.Count(script, "MODIFY COLUMN `email`"); n != 1 {
		t.Fatalf("email changes in type and nullability should give one MODIFY, got %d:\n%s", n, script)
	}
	for _, want := range []string{
		"ALTER TABLE `app`.`users` MODIFY COLUMN `email` text NOT NULL;",
		"ALTER TABLE `app`.`users` MODIFY COLUMN `active` boolean NOT NULL DEFAULT true;",
		"DROP INDEX `users_email_key` ON `app`.`users`;",
		"ALTER TABLE `app`.`users` ADD CONSTRAINT `users_org_fk` FOREIGN KEY (`org_id`) REFERENCES `app`.`orgs` (`id`) ON DELETE CASCADE;",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
}

func TestScriptAcrossEngines(t *testing.T) {
	src, _ := schemas(db.Postgres)
	_, dst := schemas(db.MySQL)
	if s := Script(Diff(src, dst), src, dst, Options{}); !strings.HasPrefix(s, "-- postgres and mysql differ") {
		t.Fatalf("got %q", s)
	}
}

func TestNoDifferences(t *testing.T) {
	src, _ := schemas(db.Postgres)
	if changes := Diff(src, src); len(changes) != 0 {
		t.Fatalf("a schema equals itself: %v", changes)
	}
	if s := Script(nil, src, src, Options{}); s != "-- no differences\n" {
		t.Fatalf("got %q", s)
	}
}

func TestScriptOrder(t *testing.T) {
	src, dst := schemas(db.Postgres)
	src.Tables["users"].Detail.ForeignKeys[0].RefTable = "zones"
	src.Tables["zones"] = &Table{Name: "zones", Kind: db.KindTable, Detail: &db.TableDetail{}, DDL: "CREATE TABLE zones (id bigint)"}
	script := Script(Diff(src, dst), src, dst, Options{Drops: true})
	pos := func(s string) int {
		i := strings.Index(script, s)
		if i < 0 {
			t.Fatalf("script lacks %q:\n%s", s, script)
		}
		return i
	}
	if pos("CREATE TABLE zones") > pos("ADD CONSTRAINT \"users_org_fk\"") {
		t.Errorf("tables must be created before foreign keys reference them:\n%s", script)
	}
	if pos("ADD COLUMN \"org_id\"") > pos("CREATE INDEX \"users_org_idx\"") {
		t.Errorf("columns must exist before their indexes:\n%s", script)
	}
	if pos("DROP INDEX \"public\".\"users_legacy_idx\"") > pos("DROP COLUMN \"legacy\"") || pos("DROP COLUMN \"legacy\"") > pos("DROP TABLE") {
		t.Errorf("drops go last, indexes before columns before tables:\n%s", script)
	}
}

func TestScriptOtherSchemaWarns(t *testing.T) {
	src, dst := schemas(db.Postgres)
	dst.Schema = "next"
	if s := Script(Diff(src, dst), src, dst, Options{}); !strings.Contains(s, "-- DDL of orgs copied from schema public: change the schema to next") {
		t.Fatalf("missing warning:\n%s", s)
	}
}

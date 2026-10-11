package editsql

import (
	"errors"
	"reflect"
	"testing"

	"github.com/farvisun/squi/internal/db"
)

var (
	pg    = db.Dialect{Driver: db.Postgres}
	my    = db.Dialect{Driver: db.MySQL}
	pgRef = db.ObjectRef{Database: "shop", Schema: "public", Name: "users"}
	myRef = db.ObjectRef{Database: "shop", Schema: "shop", Name: "users"}
)

func TestGenerate(t *testing.T) {
	changes := []Change{
		{Kind: Update, KeyCols: []string{"id"}, KeyVals: []db.Cell{db.Text("5")},
			Cols: []string{"name", "note"}, Vals: []db.Cell{db.Text("O'Brien"), db.NullCell}},
		{Kind: Insert, Cols: []string{"name"}, Vals: []db.Cell{db.Text("x")}},
		{Kind: Insert},
		{Kind: Delete, KeyCols: []string{"a", "b"}, KeyVals: []db.Cell{db.Text("1"), db.Text("2")}},
		{Kind: Update, KeyCols: []string{"Id"}, KeyVals: []db.Cell{db.Text("1")},
			Cols: []string{"path"}, Vals: []db.Cell{db.Text(`C:\tmp`)}},
	}
	tests := []struct {
		name string
		d    db.Dialect
		ref  db.ObjectRef
		want []string
	}{
		{"postgres", pg, pgRef, []string{
			`UPDATE "public"."users" SET "name" = 'O''Brien', "note" = NULL WHERE "id" = '5'`,
			`INSERT INTO "public"."users" ("name") VALUES ('x')`,
			`INSERT INTO "public"."users" DEFAULT VALUES`,
			`DELETE FROM "public"."users" WHERE "a" = '1' AND "b" = '2'`,
			`UPDATE "public"."users" SET "path" = E'C:\\tmp' WHERE "Id" = '1'`,
		}},
		{"mysql", my, myRef, []string{
			"UPDATE `shop`.`users` SET `name` = 'O''Brien', `note` = NULL WHERE `id` = '5' LIMIT 1",
			"INSERT INTO `shop`.`users` (`name`) VALUES ('x')",
			"INSERT INTO `shop`.`users` () VALUES ()",
			"DELETE FROM `shop`.`users` WHERE `a` = '1' AND `b` = '2' LIMIT 1",
			"UPDATE `shop`.`users` SET `path` = 'C:\\\\tmp' WHERE `Id` = '1' LIMIT 1",
		}},
		{"sqlserver", db.Dialect{Driver: db.SQLServer}, db.ObjectRef{Schema: "dbo", Name: "users"}, []string{
			`UPDATE TOP (1) [dbo].[users] SET [name] = 'O''Brien', [note] = NULL WHERE [id] = '5'`,
			`INSERT INTO [dbo].[users] ([name]) VALUES ('x')`,
			`INSERT INTO [dbo].[users] DEFAULT VALUES`,
			`DELETE TOP (1) FROM [dbo].[users] WHERE [a] = '1' AND [b] = '2'`,
			`UPDATE TOP (1) [dbo].[users] SET [path] = 'C:\tmp' WHERE [Id] = '1'`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Generate(tt.d, tt.ref, changes)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				for i := range got {
					if i >= len(tt.want) || got[i] != tt.want[i] {
						t.Errorf("stmt %d:\n got: %s\nwant: %s", i, got[i], tt.want[min(i, len(tt.want)-1)])
					}
				}
			}
		})
	}
}

func TestGenerateErrors(t *testing.T) {
	tests := []struct {
		name string
		ref  db.ObjectRef
		c    Change
		want error
	}{
		{"no table", db.ObjectRef{}, Change{Kind: Insert}, ErrNoTable},
		{"update without key", pgRef, Change{Kind: Update, Cols: []string{"a"}, Vals: []db.Cell{db.Text("1")}}, ErrNoKey},
		{"delete without key", pgRef, Change{Kind: Delete}, ErrNoKey},
		{"null key", pgRef, Change{Kind: Delete, KeyCols: []string{"id"}, KeyVals: []db.Cell{db.NullCell}}, ErrNullKey},
		{"empty update", pgRef, Change{Kind: Update, KeyCols: []string{"id"}, KeyVals: []db.Cell{db.Text("1")}}, ErrNoColumns},
		{"value count", pgRef, Change{Kind: Insert, Cols: []string{"a", "b"}, Vals: []db.Cell{db.Text("1")}}, ErrMismatch},
		{"key count", pgRef, Change{Kind: Delete, KeyCols: []string{"a", "b"}, KeyVals: []db.Cell{db.Text("1")}}, ErrMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Generate(pg, tt.ref, []Change{tt.c})
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

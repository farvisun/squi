package sqltext

import (
	"reflect"
	"testing"

	"github.com/farvisun/squi/internal/db"
)

const ms = db.SQLServer

func TestTokenizeSQLServer(t *testing.T) {
	tests := []struct{ src, want string }{
		{"SELECT [order id], [a]]b] FROM [dbo].[t]",
			"keyword:SELECT quoted-ident:[order id] punct:, quoted-ident:[a]]b] keyword:FROM quoted-ident:[dbo] punct:. quoted-ident:[t]"},
		{"SELECT @n, @@ROWCOUNT FROM #tmp JOIN ##g ON 1=1",
			"keyword:SELECT ident:@n punct:, ident:@@ROWCOUNT keyword:FROM ident:#tmp keyword:JOIN ident:##g keyword:ON number:1 operator:= number:1"},
		{"SELECT N'café', 'it''s', 'a\\'", "keyword:SELECT string:N'café' punct:, string:'it''s' punct:, string:'a\\'"},
		{"SELECT 1\nGO\nSELECT 2\ngo 3\nGOTO x", "keyword:SELECT number:1 delimiter:GO keyword:SELECT number:2 delimiter:go 3 keyword:GOTO ident:x"},
		{"SELECT /* a /* b */ c */ 1", "keyword:SELECT comment:/* a /* b */ c */ number:1"},
		{"SELECT TOP (5) * FROM t WITH (NOLOCK)",
			"keyword:SELECT keyword:TOP punct:( number:5 punct:) operator:* keyword:FROM ident:t keyword:WITH punct:( keyword:NOLOCK punct:)"},
	}
	for _, tt := range tests {
		if got := sig(tt.src, ms); got != tt.want {
			t.Errorf("Tokenize(%q)\n got  %s\n want %s", tt.src, got, tt.want)
		}
	}
	if got := UnquoteIdent("[a]]b]"); got != "a]b" {
		t.Errorf("UnquoteIdent = %q", got)
	}
	if got := UnquoteIdent(`"a""b"`); got != `a"b` {
		t.Errorf("UnquoteIdent = %q", got)
	}
}

func TestSplitSQLServer(t *testing.T) {
	tests := []struct {
		name, src string
		want      []string
	}{
		{"semicolons", "SELECT 1; SELECT 2;", []string{"SELECT 1", "SELECT 2"}},
		{"GO separates", "SELECT 1\nGO\nSELECT 2\nGO", []string{"SELECT 1", "SELECT 2"}},
		{"DECLARE batch kept whole", "DECLARE @n int = 5;\nSELECT * FROM t WHERE id = @n;\nGO\nSELECT 3;",
			[]string{"DECLARE @n int = 5;\nSELECT * FROM t WHERE id = @n;", "SELECT 3"}},
		{"procedure body to GO", "CREATE PROCEDURE p AS\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND\nGO\nEXEC p;",
			[]string{"CREATE PROCEDURE p AS\nBEGIN\n  SELECT 1;\n  SELECT 2;\nEND", "EXEC p"}},
		{"create or alter proc without BEGIN", "CREATE OR ALTER PROC p AS SELECT 1; SELECT 2;\nGO\nSELECT 3",
			[]string{"CREATE OR ALTER PROC p AS SELECT 1; SELECT 2;", "SELECT 3"}},
		{"BEGIN END block", "BEGIN TRY\n  SELECT 1;\n  SELECT 2;\nEND TRY\nBEGIN CATCH\n  SELECT 3;\nEND CATCH; SELECT 4",
			[]string{"BEGIN TRY\n  SELECT 1;\n  SELECT 2;\nEND TRY\nBEGIN CATCH\n  SELECT 3;\nEND CATCH", "SELECT 4"}},
		{"BEGIN TRAN is a statement", "BEGIN TRAN; UPDATE t SET a = 1 WHERE id = 2; COMMIT;",
			[]string{"BEGIN TRAN", "UPDATE t SET a = 1 WHERE id = 2", "COMMIT"}},
		{"bracket ident with semicolon", "SELECT [a;b] FROM t; SELECT 2", []string{"SELECT [a;b] FROM t", "SELECT 2"}},
	}
	for _, tt := range tests {
		if got := sqls(Split(tt.src, ms)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s:\n got  %q\n want %q", tt.name, got, tt.want)
		}
	}
}

func TestClassifySQLServer(t *testing.T) {
	tests := []struct {
		sql   string
		kind  Kind
		write bool
		destr bool
		tx    TxOp
	}{
		{"SELECT TOP (10) * FROM t WITH (NOLOCK) WHERE a = 1", KindRead, false, false, TxNone},
		{"SELECT * INTO #copy FROM t", KindWrite, true, false, TxNone},
		{"DECLARE @n int = 5; SELECT * FROM t WHERE id = @n", KindRead, false, false, TxNone},
		{"DECLARE @n int = 5 DELETE FROM t", KindWrite, true, true, TxNone},
		{"DECLARE @n int = 5; DELETE FROM t WHERE id = @n", KindWrite, true, false, TxNone},
		{"UPDATE t SET a = CASE WHEN b = 1 THEN 2 ELSE 3 END WHERE id = 5", KindWrite, true, false, TxNone},
		{"UPDATE t SET a = 1", KindWrite, true, true, TxNone},
		{"UPDATE t SET a = 1 FROM t JOIN u ON u.id = t.uid WHERE u.x = 1", KindWrite, true, false, TxNone},
		{"INSERT INTO t (a) SELECT a FROM s", KindWrite, true, false, TxNone},
		{"MERGE t USING s ON t.id = s.id WHEN MATCHED THEN UPDATE SET a = s.a WHEN NOT MATCHED BY SOURCE THEN DELETE;", KindWrite, true, false, TxNone},
		{"ALTER TABLE t ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES u (id) ON DELETE CASCADE", KindDDL, true, false, TxNone},
		{"IF @x > 0 DELETE FROM t", KindWrite, true, true, TxNone},
		{"BEGIN TRAN", KindTx, false, false, TxBegin},
		{"BEGIN TRANSACTION; UPDATE t SET a = 1 WHERE id = 1", KindWrite, true, false, TxBegin},
		{"COMMIT TRAN", KindTx, false, false, TxCommit},
		{"ROLLBACK", KindTx, false, false, TxRollback},
		{"SAVE TRANSACTION sp1", KindTx, false, false, TxNone},
		{"BEGIN TRY SELECT 1 END TRY BEGIN CATCH SELECT 2 END CATCH", KindRead, false, false, TxNone},
		{"EXEC sp_who2", KindOther, true, false, TxNone},
		{"sp_who2 'active'", KindOther, true, false, TxNone},
		{"[dbo].[orders_for_user] 7", KindOther, true, false, TxNone},
		{"KILL 53", KindOther, true, true, TxNone},
		{"DBCC SHOW_STATISTICS ('t', ix)", KindRead, false, false, TxNone},
		{"DBCC CHECKDB WITH NO_INFOMSGS", KindRead, false, false, TxNone},
		{"DBCC FREEPROCCACHE", KindOther, true, false, TxNone},
		{"RESTORE FILELISTONLY FROM DISK = 'x.bak'", KindRead, false, false, TxNone},
		{"RESTORE DATABASE d FROM DISK = 'x.bak'", KindDDL, true, true, TxNone},
		{"BULK INSERT t FROM 'x.csv'", KindWrite, true, false, TxNone},
		{"TRUNCATE TABLE t", KindDDL, true, true, TxNone},
		{"PRINT 'x'", KindRead, false, false, TxNone},
		{"WAITFOR DELAY '00:00:05'", KindRead, false, false, TxNone},
		{"SET NOCOUNT ON", KindSession, false, false, TxNone},
		{"USE shop", KindSession, false, false, TxNone},
		{"WITH c AS (SELECT 1 AS a) SELECT * FROM c", KindRead, false, false, TxNone},
		{"SELECT 1\nGO\nDROP TABLE t", KindDDL, true, true, TxNone},
		{"SELECT CASE WHEN a = 1 THEN 'x' ELSE 'y' END FROM t", KindRead, false, false, TxNone},
		{"GRANT SELECT, DELETE ON t TO u", KindDDL, true, false, TxNone},
	}
	for _, tt := range tests {
		c := Classify(tt.sql, ms)
		if c.Kind != tt.kind || c.IsWrite() != tt.write || c.Destructive != tt.destr || c.TxOp != tt.tx {
			t.Errorf("Classify(%q) = {%s write=%v destr=%v tx=%d reason=%q}; want {%s write=%v destr=%v tx=%d}",
				tt.sql, c.Kind, c.IsWrite(), c.Destructive, c.TxOp, c.Reason, tt.kind, tt.write, tt.destr, tt.tx)
		}
		if (c.Destructive || c.IsWrite()) && c.Reason == "" && c.Kind != KindWrite && c.Kind != KindDDL {
			t.Errorf("Classify(%q): write without reason", tt.sql)
		}
	}
}

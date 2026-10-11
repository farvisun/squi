package sqltext

import (
	"testing"

	"github.com/farvisun/squi/internal/db"
)

func TestClassify(t *testing.T) {
	const (
		R  = KindRead
		W  = KindWrite
		D  = KindDDL
		Tx = KindTx
		S  = KindSession
		O  = KindOther
	)
	tests := []struct {
		d       db.DriverName
		sql     string
		kind    Kind
		write   bool
		destr   bool
		tx      TxOp
		keyword string
	}{
		// reads
		{pg, "SELECT 1", R, false, false, TxNone, "SELECT"},
		{pg, "  -- c\n/* x */ select * from t", R, false, false, TxNone, "SELECT"},
		{pg, "((SELECT 1)) UNION (SELECT 2)", R, false, false, TxNone, "SELECT"},
		{pg, "SELECT * FROM t FOR UPDATE", R, false, false, TxNone, "SELECT"},
		{pg, "SELECT 'into' FROM t WHERE x IN (SELECT 1)", R, false, false, TxNone, "SELECT"},
		{pg, "SELECT a INTO new_t FROM t", W, true, false, TxNone, "SELECT"},
		{my, "SELECT a INTO @v FROM t", R, false, false, TxNone, "SELECT"},
		{my, "SELECT a FROM t INTO OUTFILE '/tmp/x'", W, true, false, TxNone, "SELECT"},
		{pg, "SHOW search_path", R, false, false, TxNone, "SHOW"},
		{my, "SHOW TABLES", R, false, false, TxNone, "SHOW"},
		{my, "DESCRIBE t", R, false, false, TxNone, "DESCRIBE"},
		{my, "DESC t", R, false, false, TxNone, "DESC"},
		{pg, "VALUES (1), (2)", R, false, false, TxNone, "VALUES"},
		{pg, "TABLE t", R, false, false, TxNone, "TABLE"},
		{pg, "WITH x AS (SELECT 1) SELECT * FROM x", R, false, false, TxNone, "SELECT"},
		{pg, "WITH RECURSIVE x(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM x) SELECT * FROM x", R, false, false, TxNone, "SELECT"},
		{pg, "WITH a AS MATERIALIZED (SELECT 1), b AS (SELECT 2) SELECT * FROM a, b", R, false, false, TxNone, "SELECT"},
		{pg, "EXPLAIN SELECT 1", R, false, false, TxNone, "EXPLAIN"},
		{pg, "EXPLAIN DELETE FROM t", R, false, false, TxNone, "EXPLAIN"},
		{pg, "EXPLAIN ANALYZE SELECT 1", R, false, false, TxNone, "EXPLAIN"},
		{pg, "EXPLAIN (ANALYZE false) DELETE FROM t", R, false, false, TxNone, "EXPLAIN"},
		{my, "EXPLAIN FORMAT=JSON SELECT 1", R, false, false, TxNone, "EXPLAIN"},
		{my, "EXPLAIN t", R, false, false, TxNone, "EXPLAIN"},
		{pg, "COPY t TO STDOUT", R, false, false, TxNone, "COPY"},
		{pg, "COPY (SELECT * FROM t) TO '/tmp/x' CSV", R, false, false, TxNone, "COPY"},
		{pg, "DECLARE c CURSOR FOR SELECT 1", R, false, false, TxNone, "DECLARE"},
		{pg, "FETCH 10 FROM c", R, false, false, TxNone, "FETCH"},
		{my, "/*!40101 SELECT 1 */", R, false, false, TxNone, "SELECT"},

		// writes
		{pg, "INSERT INTO t VALUES (1)", W, true, false, TxNone, "INSERT"},
		{pg, "insert into t select * from u", W, true, false, TxNone, "INSERT"},
		{my, "REPLACE INTO t VALUES (1)", W, true, false, TxNone, "REPLACE"},
		{my, "INSERT INTO t VALUES (1) ON DUPLICATE KEY UPDATE a = 1", W, true, false, TxNone, "INSERT"},
		{pg, "MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE", W, true, false, TxNone, "MERGE"},
		{pg, "UPDATE t SET a = 1 WHERE id = 2", W, true, false, TxNone, "UPDATE"},
		{pg, "UPDATE t SET a = 1", W, true, true, TxNone, "UPDATE"},
		{pg, "UPDATE t SET a = (SELECT b FROM u WHERE u.id = 1)", W, true, true, TxNone, "UPDATE"},
		{pg, "UPDATE t SET a = 'WHERE'", W, true, true, TxNone, "UPDATE"},
		{pg, "UPDATE t SET a = 1 -- WHERE id = 1", W, true, true, TxNone, "UPDATE"},
		{pg, "UPDATE t SET a = 1 WHERE 1=1", W, true, true, TxNone, "UPDATE"},
		{pg, "UPDATE t SET a = 1 WHERE (true)", W, true, true, TxNone, "UPDATE"},
		{pg, "UPDATE t SET a = 1 WHERE 1=1 AND id = 3", W, true, false, TxNone, "UPDATE"},
		{pg, "DELETE FROM t WHERE id = 1", W, true, false, TxNone, "DELETE"},
		{pg, "DELETE FROM t", W, true, true, TxNone, "DELETE"},
		{pg, "delete from t;", W, true, true, TxNone, "DELETE"},
		{pg, "DELETE FROM t USING u", W, true, true, TxNone, "DELETE"},
		{pg, "DELETE FROM t WHERE 'a' = 'a'", W, true, true, TxNone, "DELETE"},
		{pg, "DELETE FROM t WHERE 1 RETURNING *", W, true, true, TxNone, "DELETE"},
		{pg, "DELETE FROM t WHERE id IN (SELECT id FROM u WHERE x)", W, true, false, TxNone, "DELETE"},
		{my, "DELETE FROM t LIMIT 10", W, true, true, TxNone, "DELETE"},
		{my, "DELETE t1 FROM t1 JOIN t2 ON t1.id = t2.id WHERE t2.x = 1", W, true, false, TxNone, "DELETE"},
		{my, "UPDATE t1 JOIN t2 ON t1.id = t2.id SET t1.a = t2.a", W, true, true, TxNone, "UPDATE"},
		{pg, "WITH d AS (DELETE FROM t WHERE id = 1 RETURNING *) SELECT * FROM d", W, true, false, TxNone, "SELECT"},
		{pg, "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", W, true, true, TxNone, "SELECT"},
		{pg, "WITH x AS (SELECT id FROM u) DELETE FROM t WHERE id IN (SELECT id FROM x)", W, true, false, TxNone, "DELETE"},
		{pg, "WITH x AS (SELECT id FROM u WHERE a) DELETE FROM t", W, true, true, TxNone, "DELETE"},
		{pg, "WITH x AS (SELECT 1) UPDATE t SET a = 1 WHERE id = 1", W, true, false, TxNone, "UPDATE"},
		{pg, "EXPLAIN ANALYZE DELETE FROM t WHERE id = 1", W, true, false, TxNone, "EXPLAIN"},
		{pg, "EXPLAIN (ANALYZE, BUFFERS) UPDATE t SET a = 1", W, true, true, TxNone, "EXPLAIN"},
		{my, "EXPLAIN ANALYZE SELECT 1", R, false, false, TxNone, "EXPLAIN"},
		{pg, "COPY t FROM STDIN", W, true, false, TxNone, "COPY"},
		{pg, "COPY t (a, b) FROM '/tmp/x' WITH (FORMAT csv)", W, true, false, TxNone, "COPY"},
		{my, "LOAD DATA INFILE 'x' INTO TABLE t", W, true, false, TxNone, "LOAD"},
		{pg, "CALL p(1)", W, true, false, TxNone, "CALL"},
		{my, "call p()", W, true, false, TxNone, "CALL"},
		{pg, "DO $$ BEGIN DELETE FROM t; END $$", W, true, false, TxNone, "DO"},
		{pg, "LOCK TABLE t IN EXCLUSIVE MODE", W, true, false, TxNone, "LOCK"},
		{my, "LOCK TABLES t WRITE", W, true, false, TxNone, "LOCK"},
		{pg, "EXECUTE stmt(1)", O, true, false, TxNone, "EXECUTE"},
		{pg, "(DELETE FROM t)", W, true, true, TxNone, "DELETE"},

		// DDL
		{pg, "CREATE TABLE t (id int)", D, true, false, TxNone, "CREATE"},
		{pg, "CREATE OR REPLACE VIEW v AS SELECT 1", D, true, false, TxNone, "CREATE"},
		{pg, "CREATE INDEX CONCURRENTLY i ON t (a)", D, true, false, TxNone, "CREATE"},
		{pg, "DROP TABLE t", D, true, true, TxNone, "DROP"},
		{pg, "drop index if exists i", D, true, true, TxNone, "DROP"},
		{my, "DROP DATABASE shop", D, true, true, TxNone, "DROP"},
		{pg, "TRUNCATE t", D, true, true, TxNone, "TRUNCATE"},
		{my, "TRUNCATE TABLE t", D, true, true, TxNone, "TRUNCATE"},
		{pg, "ALTER TABLE t ADD COLUMN a int", D, true, false, TxNone, "ALTER"},
		{pg, "ALTER TABLE t DROP COLUMN a", D, true, true, TxNone, "ALTER"},
		{pg, "ALTER TABLE t DROP CONSTRAINT c", D, true, true, TxNone, "ALTER"},
		{pg, "ALTER TABLE t ALTER COLUMN a DROP DEFAULT", D, true, false, TxNone, "ALTER"},
		{pg, "ALTER TABLE t ALTER COLUMN a DROP NOT NULL", D, true, false, TxNone, "ALTER"},
		{my, "ALTER TABLE t DROP a", D, true, true, TxNone, "ALTER"},
		{my, "ALTER TABLE t DROP PRIMARY KEY", D, true, true, TxNone, "ALTER"},
		{pg, "RENAME TABLE a TO b", D, true, false, TxNone, "RENAME"},
		{pg, "COMMENT ON TABLE t IS 'x'", D, true, false, TxNone, "COMMENT"},
		{pg, "GRANT SELECT ON t TO u", D, true, false, TxNone, "GRANT"},
		{pg, "REVOKE ALL ON t FROM u", D, true, false, TxNone, "REVOKE"},
		{pg, "VACUUM ANALYZE t", D, true, false, TxNone, "VACUUM"},
		{pg, "ANALYZE t", D, true, false, TxNone, "ANALYZE"},
		{pg, "REINDEX TABLE t", D, true, false, TxNone, "REINDEX"},
		{pg, "CLUSTER t USING i", D, true, false, TxNone, "CLUSTER"},
		{pg, "REFRESH MATERIALIZED VIEW mv", D, true, false, TxNone, "REFRESH"},
		{my, "OPTIMIZE TABLE t", D, true, false, TxNone, "OPTIMIZE"},
		{my, "/*!40000 ALTER TABLE `t` DISABLE KEYS */", D, true, false, TxNone, "ALTER"},
		{my, "DELIMITER //\nCREATE PROCEDURE p() BEGIN DELETE FROM t; END//", D, true, false, TxNone, "CREATE"},

		// transactions
		{pg, "BEGIN", Tx, false, false, TxBegin, "BEGIN"},
		{pg, "begin transaction isolation level serializable", Tx, false, false, TxBegin, "BEGIN"},
		{pg, "BEGIN READ WRITE", Tx, true, false, TxBegin, "BEGIN"},
		{my, "START TRANSACTION", Tx, false, false, TxBegin, "START TRANSACTION"},
		{my, "START TRANSACTION READ WRITE", Tx, true, false, TxBegin, "START TRANSACTION"},
		{my, "START TRANSACTION READ ONLY", Tx, false, false, TxBegin, "START TRANSACTION"},
		{pg, "COMMIT", Tx, false, false, TxCommit, "COMMIT"},
		{pg, "END", Tx, false, false, TxCommit, "END"},
		{my, "END", O, false, false, TxNone, "END"},
		{pg, "COMMIT PREPARED 'x'", Tx, false, false, TxNone, "COMMIT"},
		{pg, "ROLLBACK", Tx, false, false, TxRollback, "ROLLBACK"},
		{pg, "ABORT", Tx, false, false, TxRollback, "ABORT"},
		{pg, "ROLLBACK TO SAVEPOINT s", Tx, false, false, TxNone, "ROLLBACK"},
		{my, "ROLLBACK WORK TO s", Tx, false, false, TxNone, "ROLLBACK"},
		{pg, "SAVEPOINT s", Tx, false, false, TxNone, "SAVEPOINT"},
		{pg, "RELEASE SAVEPOINT s", Tx, false, false, TxNone, "RELEASE"},
		{pg, "PREPARE TRANSACTION 'x'", Tx, false, false, TxNone, "PREPARE"},

		// session
		{pg, "SET search_path TO public", S, false, false, TxNone, "SET"},
		{pg, "SET TRANSACTION READ WRITE", S, true, false, TxNone, "SET"},
		{pg, "SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE", S, true, false, TxNone, "SET"},
		{pg, "SET default_transaction_read_only = off", S, true, false, TxNone, "SET"},
		{pg, "SET default_transaction_read_only TO 'false'", S, true, false, TxNone, "SET"},
		{pg, "SET default_transaction_read_only = on", S, false, false, TxNone, "SET"},
		{my, "SET SESSION TRANSACTION READ WRITE", S, true, false, TxNone, "SET"},
		{my, "SET @@session.transaction_read_only = 0", S, true, false, TxNone, "SET"},
		{my, "SET NAMES utf8mb4", S, false, false, TxNone, "SET"},
		{my, "/*!40101 SET NAMES utf8 */", S, false, false, TxNone, "SET"},
		{my, "USE shop", S, false, false, TxNone, "USE"},
		{pg, "RESET ALL", S, false, false, TxNone, "RESET"},
		{pg, "DISCARD ALL", S, false, false, TxNone, "DISCARD"},

		// other
		{pg, "", O, false, false, TxNone, ""},
		{pg, "-- just a comment", O, false, false, TxNone, ""},
		{pg, "LISTEN ch", O, false, false, TxNone, "LISTEN"},
		{pg, "CHECKPOINT", O, false, false, TxNone, "CHECKPOINT"},
		{my, "KILL 42", O, false, false, TxNone, "KILL"},
		{pg, "LOAD 'auto_explain'", O, false, false, TxNone, "LOAD"},
	}
	for _, tt := range tests {
		c := Classify(tt.sql, tt.d)
		if c.Kind != tt.kind || c.IsWrite() != tt.write || c.Destructive != tt.destr || c.TxOp != tt.tx || c.Keyword != tt.keyword {
			t.Errorf("Classify(%q, %s) = {%s write=%v destr=%v tx=%d kw=%q reason=%q}; want {%s write=%v destr=%v tx=%d kw=%q}",
				tt.sql, tt.d, c.Kind, c.IsWrite(), c.Destructive, c.TxOp, c.Keyword, c.Reason,
				tt.kind, tt.write, tt.destr, tt.tx, tt.keyword)
		}
		if c.Destructive && c.Reason == "" {
			t.Errorf("Classify(%q): destructive without reason", tt.sql)
		}
	}
}

func TestClassifyReasons(t *testing.T) {
	tests := map[string]string{
		"DELETE FROM t":                                "DELETE without WHERE removes all rows",
		"UPDATE t SET a = 1":                           "UPDATE without WHERE changes all rows",
		"DROP TABLE t":                                 "DROP TABLE cannot be undone",
		"TRUNCATE t":                                   "TRUNCATE removes all rows",
		"SET TRANSACTION READ WRITE":                   "changes read-only mode",
		"ALTER TABLE t DROP COLUMN a":                  "ALTER … DROP COLUMN removes it permanently",
		"EXPLAIN ANALYZE INSERT INTO t (a) VALUES (1)": "EXPLAIN ANALYZE executes the statement",
	}
	for sql, want := range tests {
		if got := Classify(sql, pg).Reason; got != want {
			t.Errorf("Classify(%q).Reason = %q, want %q", sql, got, want)
		}
	}
}

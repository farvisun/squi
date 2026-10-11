package sqltext

import (
	"slices"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// commonKeywords are keywords shared by all dialects.
var commonKeywords = []string{
	"ADD", "ALL", "ALTER", "ANALYZE", "AND", "ANY", "AS", "ASC", "BEGIN", "BETWEEN", "BOTH", "BY",
	"CALL", "CASCADE", "CASE", "CAST", "CHECK", "COLLATE", "COLUMN", "COMMENT", "COMMIT", "CONSTRAINT",
	"CREATE", "CROSS", "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "DATABASE",
	"DEFAULT", "DELETE", "DESC", "DESCRIBE", "DISTINCT", "DROP", "ELSE", "END", "ESCAPE", "EXCEPT",
	"EXECUTE", "EXISTS", "EXPLAIN", "FALSE", "FETCH", "FOR", "FOREIGN", "FROM", "FULL", "FUNCTION",
	"GRANT", "GROUP", "HAVING", "IF", "IN", "INDEX", "INNER", "INSERT", "INTERSECT", "INTERVAL", "INTO",
	"IS", "JOIN", "KEY", "LEADING", "LEFT", "LIKE", "LIMIT", "LOCK", "NATURAL", "NOT", "NULL", "OFFSET",
	"ON", "OR", "ORDER", "OUTER", "OVER", "PARTITION", "PRIMARY", "PROCEDURE", "REFERENCES", "RENAME",
	"REPLACE", "RESTRICT", "REVOKE", "RIGHT", "ROLLBACK", "ROW", "ROWS", "SAVEPOINT", "SCHEMA", "SELECT",
	"SET", "SHOW", "TABLE", "THEN", "TO", "TRAILING", "TRANSACTION", "TRIGGER", "TRUE", "TRUNCATE",
	"UNION", "UNIQUE", "UPDATE", "USING", "VALUES", "VIEW", "WHEN", "WHERE", "WINDOW", "WITH",
}

var pgKeywords = []string{
	"ABORT", "ARRAY", "ASYMMETRIC", "CLUSTER", "CONCURRENTLY", "CONFLICT", "COPY", "DEFERRABLE", "DO",
	"EXCLUDE", "EXTENSION", "FILTER", "FREEZE", "ILIKE", "INHERITS", "ISNULL", "LATERAL", "LISTEN",
	"MATERIALIZED", "NOTHING", "NOTIFY", "NOTNULL", "NULLS", "ONLY", "OWNER", "REINDEX", "REFRESH",
	"RELEASE", "RESET", "RETURNING", "SEQUENCE", "SIMILAR", "SYMMETRIC", "TABLESPACE", "TEMPORARY",
	"UNLOGGED", "VACUUM", "VARIADIC", "VERBOSE",
}

var mysqlKeywords = []string{
	"AUTO_INCREMENT", "CHANGE", "CHARSET", "DATABASES", "DELIMITER", "DIV", "DUPLICATE", "ENGINE",
	"FORCE", "FULLTEXT", "HANDLER", "IGNORE", "KILL", "LOAD", "MODIFY", "OPTIMIZE", "OUTFILE",
	"PROCESSLIST", "REGEXP", "REPAIR", "RLIKE", "SQL_CALC_FOUND_ROWS", "STRAIGHT_JOIN", "TABLES",
	"UNLOCK", "UNSIGNED", "USE", "XOR", "ZEROFILL",
}

var clickhouseKeywords = []string{
	"ARRAY", "ASOF", "ATTACH", "CODEC", "DATABASES", "DETACH", "DICTIONARY", "ENGINE", "EXCHANGE",
	"FINAL", "FORMAT", "GLOBAL", "GRANULARITY", "ILIKE", "KILL", "LIVE", "MATERIALIZED", "MUTATION",
	"OPTIMIZE", "PARTITION", "POPULATE", "PREWHERE", "SAMPLE", "SETTINGS", "SYNC", "SYSTEM", "TABLES",
	"TTL", "USE", "WATCH",
}

var mssqlKeywords = []string{
	"APPLY", "BACKUP", "BREAK", "BROWSE", "BULK", "CATCH", "CHECKPOINT", "CLUSTERED", "CONTINUE",
	"CONTAINS", "CURSOR", "DBCC", "DEALLOCATE", "DECLARE", "DENY", "DISTRIBUTED", "DUMP", "ERRLVL",
	"EXEC", "EXIT", "FILLFACTOR", "FREETEXT", "GO", "GOTO", "HOLDLOCK", "IDENTITY", "IDENTITY_INSERT",
	"INCLUDE", "KILL", "MATCHED", "MERGE", "NEXT", "NOCHECK", "NOCOUNT", "NOLOCK", "NONCLUSTERED", "OFF", "ONLY",
	"OPENJSON", "OPENQUERY", "OPENROWSET", "OUTPUT", "PERCENT", "PIVOT", "PRINT", "PROC", "RAISERROR",
	"READPAST", "RECONFIGURE", "RESTORE", "RETURN", "ROWCOUNT", "ROWLOCK", "SAVE", "SCHEMABINDING",
	"SHUTDOWN", "STATISTICS", "TABLOCK", "TEXTSIZE", "THROW", "TIES", "TOP", "TRAN", "TRY", "TRY_CAST",
	"TRY_CONVERT", "UNPIVOT", "UPDLOCK", "USE", "WAITFOR", "WHILE", "XACT_ABORT",
}

var sqliteKeywords = []string{
	"ABORT", "AFTER", "ATTACH", "AUTOINCREMENT", "BEFORE", "CONFLICT", "DEFERRABLE", "DEFERRED", "DETACH",
	"DO", "EACH", "EXCLUSIVE", "FILTER", "GLOB", "IGNORE", "IMMEDIATE", "INDEXED", "INITIALLY", "INSTEAD",
	"ISNULL", "MATERIALIZED", "NOTHING", "NOTNULL", "NULLS", "PRAGMA", "RAISE", "RECURSIVE", "REGEXP",
	"REINDEX", "RELEASE", "RETURNING", "TEMP", "TEMPORARY", "VACUUM", "VIRTUAL", "WITHOUT",
}

var (
	sqliteKeywordSet     = keywordSet(commonKeywords, sqliteKeywords)
	mssqlKeywordSet      = keywordSet(commonKeywords, mssqlKeywords)
	pgKeywordSet         = keywordSet(commonKeywords, pgKeywords)
	mysqlKeywordSet      = keywordSet(commonKeywords, mysqlKeywords)
	clickhouseKeywordSet = keywordSet(commonKeywords, clickhouseKeywords)
)

func keywordSet(lists ...[]string) map[string]bool {
	m := map[string]bool{}
	for _, l := range lists {
		for _, k := range l {
			m[k] = true
		}
	}
	return m
}

// IsKeyword reports whether word (any case) is a keyword of dialect d.
func IsKeyword(word string, d db.DriverName) bool {
	w := strings.ToUpper(word)
	switch d {
	case db.ClickHouse:
		return clickhouseKeywordSet[w]
	case db.MySQL:
		return mysqlKeywordSet[w]
	case db.SQLServer:
		return mssqlKeywordSet[w]
	case db.SQLite:
		return sqliteKeywordSet[w]
	}
	return pgKeywordSet[w]
}

// Keywords returns the sorted, upper-case keyword list of a dialect.
func Keywords(d db.DriverName) []string {
	extra := pgKeywords
	switch d {
	case db.MySQL:
		extra = mysqlKeywords
	case db.ClickHouse:
		extra = clickhouseKeywords
	case db.SQLServer:
		extra = mssqlKeywords
	case db.SQLite:
		extra = sqliteKeywords
	}
	out := append(slices.Clone(commonKeywords), extra...)
	slices.Sort(out)
	return out
}

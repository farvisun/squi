package schema

import (
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// Reserved words that must be quoted when used as identifiers.
var (
	pgReserved = wordSet(`ALL ANALYSE ANALYZE AND ANY ARRAY AS ASC ASYMMETRIC AUTHORIZATION BINARY BOTH CASE CAST
		CHECK COLLATE COLLATION COLUMN CONCURRENTLY CONSTRAINT CREATE CROSS CURRENT_CATALOG CURRENT_DATE
		CURRENT_ROLE CURRENT_SCHEMA CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER DEFAULT DEFERRABLE DESC
		DISTINCT DO ELSE END EXCEPT FALSE FETCH FOR FOREIGN FREEZE FROM FULL GRANT GROUP HAVING ILIKE IN
		INITIALLY INNER INTERSECT INTO IS ISNULL JOIN LATERAL LEADING LEFT LIKE LIMIT LOCALTIME
		LOCALTIMESTAMP NATURAL NOT NOTNULL NULL OFFSET ON ONLY OR ORDER OUTER OVERLAPS PLACING PRIMARY
		REFERENCES RETURNING RIGHT SELECT SESSION_USER SIMILAR SOME SYMMETRIC TABLE TABLESAMPLE THEN TO
		TRAILING TRUE UNION UNIQUE USER USING VARIADIC VERBOSE WHEN WHERE WINDOW WITH`)
	mssqlReserved = wordSet(`ADD ALL ALTER AND ANY AS ASC AUTHORIZATION BACKUP BEGIN BETWEEN BREAK BROWSE BULK BY
		CASCADE CASE CHECK CHECKPOINT CLOSE CLUSTERED COALESCE COLLATE COLUMN COMMIT COMPUTE CONSTRAINT
		CONTAINS CONTAINSTABLE CONTINUE CONVERT CREATE CROSS CURRENT CURRENT_DATE CURRENT_TIME
		CURRENT_TIMESTAMP CURRENT_USER CURSOR DATABASE DBCC DEALLOCATE DECLARE DEFAULT DELETE DENY DESC
		DISK DISTINCT DISTRIBUTED DOUBLE DROP DUMP ELSE END ERRLVL ESCAPE EXCEPT EXEC EXECUTE EXISTS EXIT
		EXTERNAL FETCH FILE FILLFACTOR FOR FOREIGN FREETEXT FREETEXTTABLE FROM FULL FUNCTION GOTO GRANT
		GROUP HAVING HOLDLOCK IDENTITY IDENTITY_INSERT IDENTITYCOL IF IN INDEX INNER INSERT INTERSECT INTO
		IS JOIN KEY KILL LEFT LIKE LINENO LOAD MERGE NATIONAL NOCHECK NONCLUSTERED NOT NULL NULLIF OF OFF
		OFFSETS ON OPEN OPENDATASOURCE OPENQUERY OPENROWSET OPENXML OPTION OR ORDER OUTER OVER PERCENT
		PIVOT PLAN PRECISION PRIMARY PRINT PROC PROCEDURE PUBLIC RAISERROR READ READTEXT RECONFIGURE
		REFERENCES REPLICATION RESTORE RESTRICT RETURN REVERT REVOKE RIGHT ROLLBACK ROWCOUNT ROWGUIDCOL RULE
		SAVE SCHEMA SECURITYAUDIT SELECT SEMANTICKEYPHRASETABLE SESSION_USER SET SETUSER SHUTDOWN SOME
		STATISTICS SYSTEM_USER TABLE TABLESAMPLE TEXTSIZE THEN TO TOP TRAN TRANSACTION TRIGGER TRUNCATE
		TRY_CONVERT TSEQUAL UNION UNIQUE UNPIVOT UPDATE UPDATETEXT USE USER VALUES VARYING VIEW WAITFOR
		WHEN WHERE WHILE WITH WITHIN WRITETEXT`)
	sqliteReserved = wordSet(`ADD ALL ALTER AND AS AUTOINCREMENT BETWEEN CASE CHECK COLLATE COMMIT CONSTRAINT
		CREATE CROSS CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP DEFAULT DEFERRABLE DELETE DISTINCT DROP
		ELSE ESCAPE EXCEPT EXISTS FOREIGN FROM FULL GROUP HAVING IF IN INDEX INNER INSERT INTERSECT INTO IS
		ISNULL JOIN LEFT LIMIT NATURAL NOT NOTHING NOTNULL NULL ON OR ORDER OUTER PRIMARY REFERENCES
		RETURNING RIGHT SELECT SET TABLE THEN TO TRANSACTION UNION UNIQUE UPDATE USING VALUES WHEN WHERE`)
	mysqlReserved = wordSet(`ADD ALL ALTER ANALYZE AND AS ASC BEFORE BETWEEN BIGINT BINARY BLOB BOTH BY CALL
		CASCADE CASE CHANGE CHAR CHARACTER CHECK COLLATE COLUMN CONDITION CONSTRAINT CONTINUE CONVERT CREATE
		CROSS CUME_DIST CURRENT_DATE CURRENT_TIME CURRENT_TIMESTAMP CURRENT_USER CURSOR DATABASE DATABASES
		DEC DECIMAL DECLARE DEFAULT DELAYED DELETE DENSE_RANK DESC DESCRIBE DISTINCT DIV DOUBLE DROP DUAL
		EACH ELSE ELSEIF EMPTY ENCLOSED ESCAPED EXCEPT EXISTS EXIT EXPLAIN FALSE FETCH FIRST_VALUE FLOAT
		FOR FORCE FOREIGN FROM FULLTEXT FUNCTION GENERATED GET GRANT GROUP GROUPING GROUPS HAVING IF IGNORE
		IN INDEX INFILE INNER INOUT INSERT INT INTEGER INTERSECT INTERVAL INTO IS ITERATE JOIN JSON_TABLE
		KEY KEYS KILL LAG LAST_VALUE LATERAL LEAD LEADING LEAVE LEFT LIKE LIMIT LINES LOAD LOCK LONG LOOP
		MATCH MOD NATURAL NOT NTILE NULL NUMERIC OF ON OPTIMIZE OPTION OR ORDER OUT OUTER OUTFILE OVER
		PARTITION PERCENT_RANK PRECISION PRIMARY PROCEDURE PURGE RANGE RANK READ REAL RECURSIVE REFERENCES
		REGEXP RELEASE RENAME REPEAT REPLACE REQUIRE RESTRICT RETURN REVOKE RIGHT RLIKE ROW ROWS ROW_NUMBER
		SCHEMA SCHEMAS SELECT SET SHOW SIGNAL SMALLINT SPATIAL SQL STARTING STORED STRAIGHT_JOIN SYSTEM
		TABLE TERMINATED THEN TINYINT TO TRAILING TRIGGER TRUE UNDO UNION UNIQUE UNLOCK UNSIGNED UPDATE
		USAGE USE USING VALUES VARCHAR VARYING VIRTUAL WHEN WHERE WHILE WINDOW WITH WRITE XOR`)
)

// Built-in functions offered in expression contexts.
var (
	commonFuncs = strings.Fields(`count sum avg min max coalesce nullif greatest least lower upper length
		substring trim replace round abs ceil floor mod concat left right lpad rpad md5 now row_number rank
		dense_rank lag lead first_value last_value ntile`)
	pgFuncs = strings.Fields(`date_trunc date_part to_char to_date to_timestamp age extract string_agg
		array_agg json_agg jsonb_agg json_build_object jsonb_build_object jsonb_set jsonb_extract_path_text
		to_jsonb unnest generate_series regexp_replace regexp_matches split_part position format
		gen_random_uuid nextval currval setval pg_size_pretty pg_total_relation_size string_to_array
		array_length bool_and bool_or percentile_cont clock_timestamp`)
	mysqlFuncs = strings.Fields(`ifnull if date_format str_to_date date_add date_sub datediff timestampdiff
		unix_timestamp from_unixtime curdate curtime group_concat json_extract json_object json_arrayagg
		json_unquote concat_ws substring_index locate instr char_length last_insert_id uuid found_rows
		format sha2`)
	mssqlFuncs = strings.Fields(`getdate getutcdate sysdatetime sysutcdatetime sysdatetimeoffset dateadd datediff
		datediff_big datepart datename datefromparts eomonth year month day format convert try_cast
		try_convert isnull iif choose len datalength charindex patindex stuff string_agg string_split
		concat_ws newid newsequentialid object_id object_name db_name schema_name scope_identity
		json_value json_query json_modify openjson isjson count_big checksum hashbytes quotename
		replicate reverse ltrim rtrim translate square sqrt power sign rand at_time_zone`)
	sqliteFuncs = strings.Fields(`ifnull iif instr substr printf format date time datetime julianday
		strftime unixepoch typeof quote hex unhex randomblob zeroblob random changes total_changes
		last_insert_rowid glob char unicode group_concat total json json_extract json_object json_array
		json_group_array json_group_object json_each json_tree json_type json_valid json_patch
		json_insert json_set json_remove sqlite_version likelihood ltrim rtrim sign`)
	clickhouseFuncs = strings.Fields(`if multiIf uniq uniqExact uniqCombined countIf sumIf avgIf any anyLast
		argMax argMin groupArray groupUniqArray quantile quantiles median topK arrayJoin arrayMap
		arrayFilter arrayExists has hasAny length empty notEmpty toDate toDateTime toDateTime64 toStartOfDay
		toStartOfHour toStartOfMonth toStartOfWeek toYYYYMM toYYYYMMDD toUnixTimestamp now64 today
		yesterday dateDiff formatDateTime parseDateTimeBestEffort toString toInt32 toInt64 toUInt32
		toUInt64 toFloat64 toDecimal64 toTypeName formatReadableSize formatReadableQuantity splitByChar
		position match extract replaceAll replaceRegexpAll JSONExtractString JSONExtractInt visitParamExtractString
		cityHash64 sipHash64 rand generateUUIDv4 dictGet tuple mapKeys mapValues windowFunnel retention`)
)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

func functions(d db.DriverName) []string {
	if d == db.ClickHouse {
		return append(commonFuncs[:len(commonFuncs):len(commonFuncs)], clickhouseFuncs...)
	}
	if d == db.MySQL {
		return append(commonFuncs[:len(commonFuncs):len(commonFuncs)], mysqlFuncs...)
	}
	if d == db.SQLServer {
		return append(commonFuncs[:len(commonFuncs):len(commonFuncs)], mssqlFuncs...)
	}
	if d == db.SQLite {
		return append(commonFuncs[:len(commonFuncs):len(commonFuncs)], sqliteFuncs...)
	}
	return append(commonFuncs[:len(commonFuncs):len(commonFuncs)], pgFuncs...)
}

// needsQuote reports whether name must be quoted to be used as an
// identifier: Postgres folds unquoted names to lower case, so anything but
// lower-case letters, digits, _ and $ needs quotes; reserved words always do.
// MySQL, SQL Server and SQLite names are case-insensitive.
func needsQuote(name string, d db.DriverName) bool {
	if name == "" {
		return true
	}
	reserved, caseBlind := pgReserved, d.MySQLStyle() || d == db.SQLServer || d == db.SQLite
	switch {
	case d.MySQLStyle():
		reserved = mysqlReserved
	case d == db.SQLServer:
		reserved = mssqlReserved
	case d == db.SQLite:
		reserved = sqliteReserved
	}
	if reserved[strings.ToUpper(name)] {
		return true
	}
	digits := true
	for i, r := range name {
		switch {
		case r >= '0' && r <= '9':
			if i == 0 && !d.MySQLStyle() {
				return true
			}
		case r == '_', r >= 'a' && r <= 'z':
			digits = false
		case r == '$' && i > 0:
		case (r == '@' || r == '#') && i == 0 && d == db.SQLServer:
			return true // would read as a variable or temp table
		case r >= 'A' && r <= 'Z' && caseBlind:
			digits = false
		case r >= 0x80:
			digits = false
		default:
			return true
		}
	}
	return digits && d.MySQLStyle()
}

// ident renders name as an identifier, quoting it if needed or forced.
func ident(d db.Dialect, name string, force bool) string {
	if force || needsQuote(name, d.Driver) {
		return d.QuoteIdent(name)
	}
	return name
}

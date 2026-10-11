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
)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

func functions(d db.DriverName) []string {
	if d == db.MySQL {
		return append(commonFuncs[:len(commonFuncs):len(commonFuncs)], mysqlFuncs...)
	}
	return append(commonFuncs[:len(commonFuncs):len(commonFuncs)], pgFuncs...)
}

// needsQuote reports whether name must be quoted to be used as an
// identifier: Postgres folds unquoted names to lower case, so anything but
// lower-case letters, digits, _ and $ needs quotes; reserved words always do.
func needsQuote(name string, d db.DriverName) bool {
	if name == "" {
		return true
	}
	reserved := pgReserved
	if d == db.MySQL {
		reserved = mysqlReserved
	}
	if reserved[strings.ToUpper(name)] {
		return true
	}
	digits := true
	for i, r := range name {
		switch {
		case r >= '0' && r <= '9':
			if i == 0 && d != db.MySQL {
				return true
			}
		case r == '_', r >= 'a' && r <= 'z':
			digits = false
		case r == '$' && i > 0:
		case r >= 'A' && r <= 'Z' && d == db.MySQL:
			digits = false
		case r >= 0x80:
			digits = false
		default:
			return true
		}
	}
	return digits && d == db.MySQL
}

// ident renders name as an identifier, quoting it if needed or forced.
func ident(d db.Dialect, name string, force bool) string {
	if force || needsQuote(name, d.Driver) {
		return d.QuoteIdent(name)
	}
	return name
}

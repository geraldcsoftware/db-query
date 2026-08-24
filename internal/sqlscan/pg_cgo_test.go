//go:build cgo

package sqlscan

import "testing"

func TestClassifyPostgres(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want Class
	}{
		{"select", "SELECT id, name FROM people WHERE name = 'Ada'", ClassRead},
		{"show", "SHOW search_path", ClassRead},
		{"insert", "INSERT INTO t (a) VALUES (1)", ClassWrite},
		{"update", "UPDATE t SET a = 1 WHERE id = 2", ClassWrite},
		{"drop", "DROP TABLE t", ClassDestructive},
		{"truncate", "TRUNCATE t", ClassDestructive},
		{"delete", "DELETE FROM t WHERE id = 1", ClassDestructive},
		{"grant", "GRANT ALL ON t TO public", ClassAdmin},

		// Schema changes are destructive whatever their surface verb: the
		// boundary is data versus schema, not the presence of the word DROP.
		// ALTER TABLE t DROP COLUMN x loses a column and its data while
		// parsing as an ordinary AlterTableStmt, so a subtype walk is the
		// wrong place to draw the line.
		{"create table", "CREATE TABLE t (a int)", ClassDestructive},
		{"alter table add column", "ALTER TABLE t ADD COLUMN x int", ClassDestructive},
		{"alter table drop column", "ALTER TABLE t DROP COLUMN x", ClassDestructive},
		{"alter table change type", "ALTER TABLE t ALTER COLUMN x TYPE text", ClassDestructive},
		{"alter table detach partition", "ALTER TABLE t DETACH PARTITION p", ClassDestructive},
		{"create index", "CREATE INDEX i ON t (a)", ClassDestructive},
		{"create view", "CREATE VIEW v AS SELECT 1", ClassDestructive},
		{"replace view", "CREATE OR REPLACE VIEW v AS SELECT 2", ClassDestructive},
		{"create materialized view", "CREATE MATERIALIZED VIEW mv AS SELECT 1", ClassDestructive},
		{"create table as", "CREATE TABLE u AS SELECT * FROM t", ClassDestructive},
		{"create function", "CREATE FUNCTION f() RETURNS int AS 'SELECT 1' LANGUAGE sql", ClassDestructive},
		{"rename table", "ALTER TABLE t RENAME TO u", ClassDestructive},
		{"rename column", "ALTER TABLE t RENAME COLUMN a TO b", ClassDestructive},

		// SELECT ... INTO creates and populates a table, but the grammar
		// gives it a bare SelectStmt carrying an IntoClause. Read the top
		// node alone and it passes for an ordinary read.
		{"select into creates a table", "SELECT * INTO u FROM t", ClassDestructive},
		{"select into in a CTE body", "WITH g AS (SELECT 1 AS n) SELECT * INTO u FROM g", ClassDestructive},

		// Data movement that leaves the schema alone stays a write.
		{"copy into a table", "COPY t FROM '/tmp/x'", ClassWrite},
		{"copy out of a query", "COPY (SELECT * FROM t) TO '/tmp/x'", ClassWrite},
		{"refresh materialized view", "REFRESH MATERIALIZED VIEW mv", ClassWrite},

		// The cases a keyword scan gets wrong, in both directions.
		{"delete carried in a CTE", "WITH g AS (DELETE FROM t RETURNING *) SELECT * FROM g", ClassDestructive},
		{"insert carried in a CTE", "WITH g AS (INSERT INTO t VALUES (1) RETURNING *) SELECT * FROM g", ClassWrite},
		{"DROP inside a string literal is not a drop", "SELECT 'DROP TABLE t' AS s", ClassRead},
		{"an identifier containing delete is not a delete", "SELECT * FROM delete_log", ClassRead},
		{"a commented-out drop is not a drop", "-- DROP TABLE t\nSELECT 1", ClassRead},
		{"a block-commented delete is not a delete", "/* DELETE FROM t */ SELECT 1", ClassRead},
		{"multi-statement takes the worst", "SELECT 1; DROP TABLE t", ClassDestructive},

		// EXPLAIN plans; EXPLAIN ANALYZE executes.
		{"explain select is a read", "EXPLAIN SELECT * FROM t", ClassRead},
		{"explain analyze delete deletes", "EXPLAIN ANALYZE DELETE FROM t", ClassDestructive},
		{"explain analyze select still executes", "EXPLAIN ANALYZE SELECT 1", ClassWrite},

		// The three confirmed ways to escape the read-only GUC.
		{"set the guc off", "SET default_transaction_read_only = off", ClassAdmin},
		{"set transaction read write", "BEGIN; SET TRANSACTION READ WRITE; SELECT 1", ClassAdmin},
		{"session characteristics", "SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE", ClassAdmin},
		{"set_config reaches it from a plain select", "SELECT set_config('default_transaction_read_only','off',false)", ClassAdmin},
		{
			// The argument is computed, but the function name in a parse tree
			// is an identifier, so the call is still visible.
			"a computed argument does not hide the call",
			"SELECT set_config('default_' || 'transaction_read_only','off',false)", ClassAdmin,
		},
		{"schema-qualified is matched too", "SELECT pg_catalog.set_config('x','y',false)", ClassAdmin},

		// Functions whose call is itself the act.
		{"terminate backend", "SELECT pg_terminate_backend(123)", ClassAdmin},
		{"dblink_exec runs sql elsewhere", "SELECT dblink_exec('dbname=x','DROP TABLE t')", ClassAdmin},
		{"query_to_xml executes its text argument", "SELECT query_to_xml('DROP TABLE t', true, true, '')", ClassAdmin},

		// Unrecognised statement types deny rather than pass.
		{"a DO block is opaque", "DO $$ BEGIN EXECUTE 'DROP TABLE t'; END $$", ClassOpaque},
		{"unparseable is opaque", "SELECT FROM WHERE ((((", ClassOpaque},
		{"copy from program", "COPY t FROM PROGRAM 'sh -c whoami'", ClassWrite},
		{"merge", "MERGE INTO t USING u ON t.id=u.id WHEN MATCHED THEN DELETE", ClassWrite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyPostgres(tt.sql)
			if got.Class != tt.want {
				t.Errorf("got %s, want %s (statements: %+v)", got.Class, tt.want, got.Statements)
			}
			if got.Mechanism != MechanismParser {
				t.Errorf("mechanism: got %q, want %q", got.Mechanism, MechanismParser)
			}
		})
	}
}

// TestClassifyPostgresAllowsOrdinaryReads guards the direction that actually
// decides whether the tool is usable. Under a fail-closed policy a classifier
// that refuses ordinary SELECTs gets switched off, so these must all pass.
//
// It runs the whole pipeline, normaliser included, rather than the classifier
// alone. Testing the classifier by itself is what let a normaliser bug through:
// it rewrote the colons in a time format as placeholders, and the corpus never
// saw it because the corpus never called it.
func TestClassifyPostgresAllowsOrdinaryReads(t *testing.T) {
	// A bound parameter, since the normaliser only engages when one exists.
	params := map[string]string{"who": "Ada"}
	reads := []string{
		"SELECT DISTINCT ON (a) a, b FROM t ORDER BY a, b",
		"SELECT * FROM generate_series(1,10) WITH ORDINALITY",
		"SELECT * FROM t TABLESAMPLE SYSTEM (10)",
		"SELECT * FROM t, LATERAL (SELECT * FROM u WHERE u.id=t.id) s",
		"SELECT * FROM jsonb_to_recordset('[]'::jsonb) AS x(a int, b text)",
		"SELECT a, b FROM t GROUP BY GROUPING SETS ((a),(b))",
		"SELECT * FROM t WHERE tsv @@ to_tsquery('english','cat & dog')",
		"SELECT 't'::regclass::oid",
		"SELECT arr[1:3] FROM t",
		"SELECT sum(x) OVER (ORDER BY a GROUPS BETWEEN 1 PRECEDING AND CURRENT ROW) FROM t",
		"SELECT count(*) FILTER (WHERE a > 1) FROM t",
		"SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY a) FROM t",
		"WITH RECURSIVE r AS (SELECT 1 AS n UNION ALL SELECT n+1 FROM r WHERE n < 5) SELECT * FROM r",
		"SELECT * FROM t WHERE (a,b) > (1,2)",
		"SELECT * FROM t WHERE a IS DISTINCT FROM b",
		"SELECT now() - INTERVAL '1 day'",
		"SELECT $tag$hello$tag$ AS s",
		"SELECT a::text COLLATE \"C\" FROM t",
		"SELECT jsonb_path_query(data, '$.a[*]') FROM t",
		"SELECT * FROM xmltable('/r' PASSING x COLUMNS a int PATH 'a')",
		"SELECT * FROM t FOR UPDATE",

		// Colons that are not placeholders. Each of these was refused before
		// the normaliser learned to leave them alone.
		"SELECT to_char(created, 'DD Mon, HH:MM:SS') FROM t",
		"SELECT somecol::date AS date, sometext::jsonb AS jsoncontent FROM t",
		"SELECT arr[1:3] FROM t",
		"SELECT arr[1:upper] FROM t",
		"SELECT * FROM t WHERE url = 'https://x/y'",
		"SELECT 'it''s 10:30' AS s",
		"SELECT 1 -- note: something\n",
		"SELECT E'a\\'b:c' AS s",

		// And one that must still be normalised, or the grammar cannot parse it.
		"SELECT * FROM t WHERE name = :'who'",
	}
	for _, sql := range reads {
		if got := ClassifyPostgres(NormalisePlaceholders(sql, params, DialectPostgres)); got.Class != ClassRead {
			t.Errorf("falsely refused an ordinary read as %s: %s\n  %+v", got.Class, sql, got.Statements)
		}
	}
}

package tui

import (
	"strings"
	"sync"
	"testing"

	"github.com/geraldcsoftware/db-query/internal/schema"
)

func col(name, typ string) schema.Column { return schema.Column{Name: name, DataType: typ} }

// testCatalogue is a small two-schema catalogue with a name deliberately
// repeated across schemas, so the ambiguous case is covered by default.
func testCatalogue() []schema.Table {
	return []schema.Table{
		{Schema: "public", Name: "customers", Columns: []schema.Column{
			col("id", "bigint"), col("name", "varchar"), col("created_at", "timestamptz"),
		}},
		{Schema: "public", Name: "orders", Columns: []schema.Column{
			col("id", "bigint"), col("customer_id", "bigint"), col("amount", "numeric"),
		}},
		{Schema: "dbo", Name: "Cardholder", Columns: []schema.Column{
			col("CardholderId", "int"), col("Pan", "char(19)"),
		}},
		{Schema: "archive", Name: "orders", Columns: []schema.Column{col("id", "bigint")}},
	}
}

func loadedSource() *schemaSource {
	s := newSchemaSource()
	s.setTables(testCatalogue())
	return s
}

// words is the word of every candidate, in the order the source offered them.
func words(items []map[string]any) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i], _ = it["word"].(string)
	}
	return out
}

func has(items []map[string]any, word string) bool {
	for _, w := range words(items) {
		if w == word {
			return true
		}
	}
	return false
}

func TestScopeOfReadsEveryTableAQueryNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  []tableRef
	}{
		{"bare table", []string{"select * from customers"}, []tableRef{{name: "customers"}}},
		{"alias", []string{"select * from customers c"}, []tableRef{{name: "customers", alias: "c"}}},
		{"as alias", []string{"select * from customers as c"}, []tableRef{{name: "customers", alias: "c"}}},
		{"join", []string{"from orders o join customers c on c.id = o.customer_id"},
			[]tableRef{{name: "orders", alias: "o"}, {name: "customers", alias: "c"}}},
		{"comma list", []string{"from orders o, customers c where 1=1"},
			[]tableRef{{name: "orders", alias: "o"}, {name: "customers", alias: "c"}}},
		{"clause ends the list", []string{"from customers where name is null"},
			[]tableRef{{name: "customers"}}},
		{"across lines", []string{"select c.id", "from customers c", "where c.id > 1"},
			[]tableRef{{name: "customers", alias: "c"}}},
		{"qualified and quoted", []string{`from "public"."orders" o`},
			[]tableRef{{name: `"public"."orders"`, alias: "o"}}},
		{"bracketed", []string{"from [dbo].[Cardholder] ch"},
			[]tableRef{{name: "[dbo].[Cardholder]", alias: "ch"}}},
		{"comment is not scope", []string{"-- from customers c", "select 1"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := scopeOf(tc.lines)
			if len(got) != len(tc.want) {
				t.Fatalf("scopeOf = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ref %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestQualifierResolvesThroughAnAliasDeclaredElsewhere is the case the whole
// buffer is scanned for: the clause introducing an alias is rarely on the line
// the alias is used on.
func TestQualifierResolvesThroughAnAliasDeclaredElsewhere(t *testing.T) {
	s := loadedSource()
	buf := []string{"select c.", "from customers c", "where c.id > 1"}

	got := s.Candidates("c", "", buf)
	for _, want := range []string{"id", "name", "created_at"} {
		if !has(got, want) {
			t.Errorf("column %q missing behind the alias: %v", want, words(got))
		}
	}
	if has(got, "amount") {
		t.Errorf("a column of another table came back behind the alias: %v", words(got))
	}
	if has(got, "SELECT") {
		t.Errorf("keywords were offered behind a qualifier: %v", words(got))
	}
}

func TestQualifierResolvesATableNameWithoutAnAlias(t *testing.T) {
	s := loadedSource()
	for _, qualifier := range []string{"customers", "public.customers", "CUSTOMERS"} {
		got := s.Candidates(qualifier, "na", []string{"select customers.na from customers"})
		if !has(got, "name") {
			t.Errorf("qualifier %q did not resolve to the table: %v", qualifier, words(got))
		}
	}
	if got := s.Candidates("nosuch", "", []string{"select nosuch."}); len(got) != 0 {
		t.Errorf("an unknown qualifier offered %v, want nothing", words(got))
	}
}

// TestColumnsCarryTheirTypeInMenu: kind is one letter from a fixed set and
// cannot hold a type, so menu is where the popup shows it.
func TestColumnsCarryTheirTypeInMenu(t *testing.T) {
	s := loadedSource()
	got := s.Candidates("c", "amo", []string{"select c.amo from orders c"})
	if len(got) != 1 {
		t.Fatalf("got %v, want just amount", words(got))
	}
	if got[0]["menu"] != "numeric" {
		t.Errorf("menu = %v, want the column's type", got[0]["menu"])
	}
	if got[0]["kind"] != "m" {
		t.Errorf("kind = %v, want m for a column", got[0]["kind"])
	}
	if info, _ := got[0]["info"].(string); !strings.Contains(info, "public.orders.amount") {
		t.Errorf("info = %q, want the qualified column", info)
	}
}

// TestUnqualifiedOffersTheQuerysOwnColumnsButNotEveryTables: a column of a
// table the statement never mentions is not one the statement could select.
func TestUnqualifiedOffersTheQuerysOwnColumnsButNotEveryTables(t *testing.T) {
	s := loadedSource()
	buf := []string{"select cust", "from customers c"}
	got := s.Candidates("", "cust", buf)

	if !has(got, "customers") {
		t.Errorf("the table itself was not offered: %v", words(got))
	}
	if has(got, "customer_id") {
		t.Errorf("a column of a table the query never names was offered: %v", words(got))
	}

	// With that table in scope, its columns are offered.
	inScope := s.Candidates("", "cre", buf)
	if !has(inScope, "created_at") {
		t.Errorf("an in-scope column was not offered: %v", words(inScope))
	}

	// Order is the tie-break Neovim's own scoring works from: the statement's
	// own columns, then tables, then keywords.
	mixed := s.Candidates("", "cu", []string{"select cu from orders"})
	iCol, iTable, iKeyword := indexOf(mixed, "customer_id"), indexOf(mixed, "customers"), indexOf(mixed, "COUNT")
	if iCol < 0 || iTable < 0 || iKeyword < 0 {
		t.Fatalf("expected a column, a table and a keyword, got %v", words(mixed))
	}
	if !(iCol < iTable && iTable < iKeyword) {
		t.Errorf("order = %v, want the column before the table before the keyword", words(mixed))
	}
}

func indexOf(items []map[string]any, word string) int {
	for i, w := range words(items) {
		if w == word {
			return i
		}
	}
	return -1
}

// TestAnEmptyPrefixOnlyAnswersAQualifier: a dot is a question, a space is not.
func TestAnEmptyPrefixOnlyAnswersAQualifier(t *testing.T) {
	s := loadedSource()
	if got := s.Candidates("", "", []string{"select "}); len(got) != 0 {
		t.Errorf("an unqualified empty prefix offered %d candidates, want none", len(got))
	}
	got := s.Candidates("c", "", []string{"select c. from customers c"})
	if len(got) != 3 {
		t.Errorf("a qualified empty prefix offered %v, want every column", words(got))
	}
}

// TestFuzzyMatchIsNeovimsOwnMembershipTest: filtering more strictly here would
// drop candidates Neovim would have kept once completeopt has fuzzy.
func TestFuzzyMatchIsNeovimsOwnMembershipTest(t *testing.T) {
	for _, tc := range []struct {
		candidate, prefix string
		want              bool
	}{
		{"CardholderId", "card", true},  // case is ignored
		{"CardholderId", "chid", true},  // characters may be skipped
		{"CardholderId", "dhrc", false}, // but not reordered
		{"created_at", "cat", true},
		{"orders", "", true},
		{"id", "identifier", false},
	} {
		if got := fuzzyMatch(tc.candidate, tc.prefix); got != tc.want {
			t.Errorf("fuzzyMatch(%q, %q) = %v, want %v", tc.candidate, tc.prefix, got, tc.want)
		}
	}
}

// TestIdentifiersKeepTheDatabasesOwnCase: a lower-case prefix has to reach a
// PascalCase column, and it must arrive spelled the way the database holds it.
func TestIdentifiersKeepTheDatabasesOwnCase(t *testing.T) {
	s := loadedSource()
	got := s.Candidates("ch", "card", []string{"select ch.card from dbo.Cardholder ch"})
	if !has(got, "CardholderId") {
		t.Fatalf("got %v, want the column as the database spells it", words(got))
	}
	for _, w := range words(got) {
		if w == "cardholderid" || w == "cardHolderId" {
			t.Errorf("the column was re-cased to %q, which is not a column that exists", w)
		}
	}
}

// TestKeywordsFollowTheCaseBeingTypedIn is the opposite rule, and it applies to
// keywords alone because a keyword's case is nobody's to be wrong about.
func TestKeywordsFollowTheCaseBeingTypedIn(t *testing.T) {
	for _, tc := range []struct{ prefix, want string }{
		{"sel", "select"},
		{"SEL", "SELECT"},
		{"Wh", "Where"},
	} {
		got := keywordInTypedCase(tc.prefix, strings.ToUpper(tc.want))
		if got != tc.want {
			t.Errorf("keywordInTypedCase(%q) = %q, want %q", tc.prefix, got, tc.want)
		}
	}
	// A fuzzy match has no typed head to preserve, so the keyword stands.
	if got := keywordInTypedCase("dst", "DISTINCT"); got != "DISTINCT" {
		t.Errorf("a skipped-character match was rewritten to %q", got)
	}
}

// TestAmbiguousTableNamesAreOfferedQualified: inserting the bare name would
// name two tables, so only the qualified form identifies it.
func TestAmbiguousTableNamesAreOfferedQualified(t *testing.T) {
	s := loadedSource()
	got := s.Candidates("", "order", []string{"select order"})
	for _, want := range []string{"public.orders", "archive.orders"} {
		if !has(got, want) {
			t.Errorf("%q missing: %v", want, words(got))
		}
	}
	if has(got, "orders") {
		t.Errorf("the bare, ambiguous name was offered: %v", words(got))
	}
	// A name that occurs once keeps its plain form.
	if plain := s.Candidates("", "custo", []string{"select custo"}); !has(plain, "customers") {
		t.Errorf("an unambiguous table was not offered plain: %v", words(plain))
	}
}

// TestAColdCacheStillCompletesKeywords: nothing introspected yet is not an
// error, it is simply less to offer.
func TestAColdCacheStillCompletesKeywords(t *testing.T) {
	s := newSchemaSource()
	got := s.Candidates("", "sel", []string{"sel"})
	if !has(got, "select") {
		t.Errorf("a cold source offered %v, want the keywords", words(got))
	}
	if q := s.Candidates("c", "", []string{"select c. from customers c"}); len(q) != 0 {
		t.Errorf("a cold source resolved a qualifier to %v", words(q))
	}
}

// TestReplacingTheCatalogueMidSessionTakesEffect is the F2 database switch: the
// columns of the database just left must stop being offered.
func TestReplacingTheCatalogueMidSessionTakesEffect(t *testing.T) {
	s := loadedSource()
	buf := []string{"select na from customers"}
	if !has(s.Candidates("", "na", buf), "name") {
		t.Fatal("the first catalogue was not in use")
	}

	s.setTables([]schema.Table{{Schema: "public", Name: "widgets", Columns: []schema.Column{col("label", "text")}}})
	after := s.Candidates("", "na", buf)
	if has(after, "name") {
		t.Errorf("a column of the database just left survived the switch: %v", words(after))
	}
	if !has(s.Candidates("", "wid", []string{"select wid"}), "widgets") {
		t.Error("the new catalogue's tables are not being offered")
	}
}

// TestCandidatesToleratesAConcurrentSwitch: the handler answers on the
// goroutine Neovim's requests arrive on, and a database switch replaces the
// catalogue on the event loop's. Run under -race, this is the check that the
// two are safe together.
func TestCandidatesToleratesAConcurrentSwitch(t *testing.T) {
	s := loadedSource()
	buf := []string{"select na from customers c"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			s.Candidates("", "na", buf)
			s.Candidates("c", "id", buf)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			s.setTables(testCatalogue())
			s.setTables(nil)
		}
	}()
	wg.Wait()
}

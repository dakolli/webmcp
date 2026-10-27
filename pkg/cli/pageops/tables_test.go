package pageops

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTablesAspectRegistered pins the registry entry: tables validates, owns its
// own caps, and is named by the shared unknown-what error.
func TestTablesAspectRegistered(t *testing.T) {
	if err := ValidateAspect("tables"); err != nil {
		t.Fatalf("ValidateAspect(tables) = %v, want nil", err)
	}

	selfCapped := false
	for _, a := range aspectEntries {
		if a.name == "tables" {
			selfCapped = a.selfCapped
		}
	}
	if !selfCapped {
		t.Fatal("tables entry is not self-capped")
	}

	err := ValidateAspect("not-a-table")
	if err == nil || !strings.Contains(err.Error(), "tables") {
		t.Fatalf("unknown-what error = %v, want it to list tables", err)
	}
}

// TestShapeTablesCaps pins the table, row and column caps and the truncation
// signal, the dropped counts, plus the empty-array shape of a table within the
// caps.
func TestShapeTablesCaps(t *testing.T) {
	t.Run("table cap", func(t *testing.T) {
		raw := rawTables{TableTotal: 25, Tables: make([]rawTable, 25)}
		got := shapeTables(raw)
		if len(got.Tables) != tableMaxTables {
			t.Fatalf("tables = %d, want %d", len(got.Tables), tableMaxTables)
		}
		if !got.Truncated {
			t.Fatal("truncated = false, want true")
		}
		if got.TablesDropped != 5 {
			t.Fatalf("tables_dropped = %d, want 5", got.TablesDropped)
		}
		if got.RowsDropped != 0 {
			t.Fatalf("rows_dropped = %d, want 0", got.RowsDropped)
		}
	})

	t.Run("row cap", func(t *testing.T) {
		rows := make([][]string, 600)
		for i := range rows {
			rows[i] = []string{"a", "b", "c"}
		}
		raw := rawTables{
			TableTotal: 1,
			Tables: []rawTable{{
				Rows:     rows,
				RowTotal: 600,
				ColTotal: 3,
			}},
		}
		got := shapeTables(raw)
		if len(got.Tables) != 1 {
			t.Fatalf("tables = %d, want 1", len(got.Tables))
		}
		if len(got.Tables[0].Rows) != tableMaxRows {
			t.Fatalf("rows = %d, want %d", len(got.Tables[0].Rows), tableMaxRows)
		}
		if !got.Truncated {
			t.Fatal("truncated = false, want true")
		}
		if got.RowsDropped != 100 {
			t.Fatalf("rows_dropped = %d, want 100", got.RowsDropped)
		}
		if got.TablesDropped != 0 {
			t.Fatalf("tables_dropped = %d, want 0", got.TablesDropped)
		}
	})

	t.Run("column cap", func(t *testing.T) {
		row := make([]string, 60)
		for i := range row {
			row[i] = "cell"
		}
		raw := rawTables{
			TableTotal: 1,
			Tables: []rawTable{{
				Headers:  make([]string, 60),
				Rows:     [][]string{row},
				RowTotal: 1,
				ColTotal: 60,
			}},
		}
		got := shapeTables(raw)
		if len(got.Tables[0].Headers) != tableMaxColumns {
			t.Fatalf("headers = %d, want %d", len(got.Tables[0].Headers), tableMaxColumns)
		}
		if len(got.Tables[0].Rows[0]) != tableMaxColumns {
			t.Fatalf("row cells = %d, want %d", len(got.Tables[0].Rows[0]), tableMaxColumns)
		}
		if !got.Truncated {
			t.Fatal("truncated = false, want true")
		}
	})

	t.Run("within caps keeps empty arrays", func(t *testing.T) {
		raw := rawTables{TableTotal: 1, Tables: []rawTable{{RowTotal: 0, ColTotal: 0}}}
		got := shapeTables(raw)
		if got.Truncated {
			t.Fatal("truncated = true, want false")
		}
		if got.TablesDropped != 0 || got.RowsDropped != 0 {
			t.Fatalf("dropped counts = %d/%d, want 0/0", got.TablesDropped, got.RowsDropped)
		}
		if got.Tables[0].Headers == nil || got.Tables[0].Rows == nil || got.Tables[0].SpanInfo == nil {
			t.Fatal("empty collections must marshal as [], not null")
		}
	})
}

// TestShapeTablesDroppedWire pins that the truncation counts are always present
// in the JSON envelope, so a reader never has to infer them.
func TestShapeTablesDroppedWire(t *testing.T) {
	const probe = `{"table_total":25,"tables":[
		{"row_total":600,"col_total":3,"rows":[["a","b","c"]],"headers":[],"span_info":[]}
	]}`

	var raw rawTables
	if err := json.Unmarshal([]byte(probe), &raw); err != nil {
		t.Fatalf("decode probe: %v", err)
	}

	out, err := json.Marshal(shapeTables(raw))
	if err != nil {
		t.Fatalf("marshal shaped tables: %v", err)
	}
	for _, want := range []string{`"tables_dropped":24`, `"rows_dropped":599`, `"truncated":true`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("envelope %s is missing %s", out, want)
		}
	}
}

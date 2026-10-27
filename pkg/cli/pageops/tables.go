package pageops

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dakolli/webmcp/pkg/browsers"
)

const (
	// tableMaxTables bounds how many tables one response reports; the dropped
	// remainder is reported through truncated and tables_dropped.
	tableMaxTables = 20
	// tableMaxRows bounds the data rows reported per table; the dropped
	// remainder is reported through truncated and rows_dropped.
	tableMaxRows = 500
	// tableMaxColumns bounds the cells reported per row and the header width.
	tableMaxColumns = 50
	// tableMaxSpans bounds the merged-cell annotations reported per table.
	tableMaxSpans = 200

	// tableSafetyTables bounds how many tables the in-page probe collects before
	// the Go-side cap; the true total is still reported for truncation.
	tableSafetyTables = 100
	// tableSafetyRows bounds how many rows the in-page probe collects per table.
	tableSafetyRows = 2000
	// tableSafetyColumns bounds how many cells the in-page probe collects per
	// row.
	tableSafetyColumns = 200
	// tableSafetySpans bounds how many merged-cell annotations the in-page probe
	// collects per table.
	tableSafetySpans = 500
)

// cssPathJS builds a valid CSS selector for an element, preferring an id and
// falling back to a short :nth-of-type chain. It is shared by the table and form
// probes.
const cssPathJS = `
  const cssPath = (el) => {
    const parts = [];
    let cur = el;
    while (cur && cur.nodeType === 1 && parts.length < 8) {
      let tag = cur.tagName.toLowerCase();
      if (cur.id) {
        const esc = (window.CSS && CSS.escape) ? CSS.escape(cur.id) : cur.id.replace(/[^A-Za-z0-9_-]/g, '\\$&');
        parts.unshift(tag + '#' + esc);
        break;
      }
      const parent = cur.parentElement;
      if (parent) {
        const same = [...parent.children].filter((c) => c.tagName === cur.tagName);
        if (same.length > 1) tag += ':nth-of-type(' + (same.indexOf(cur) + 1) + ')';
      }
      parts.unshift(tag);
      cur = parent;
    }
    return parts.join(' > ');
  };
`

// tablesProbeJS reads every table-like container in one evaluation: native
// <table> elements plus ARIA role="table" and role="grid" containers, whose
// rows and cells are addressed through role attributes. The caps passed in are
// the probe's safety bounds; the response's true totals let the Go shaper report
// truncation accurately.
const tablesProbeJS = `(maxTables, maxRows, maxColumns, maxSpans) => {` + cssPathJS + `
  const norm = (s) => (s || '').replace(/\s+/g, ' ').trim();
  const role = (el) => (el.getAttribute('role') || '').toLowerCase();
  const tables = [...document.querySelectorAll('table, [role=table], [role=grid]')];
  const described = tables.slice(0, maxTables).map((t) => {
    const isTable = t.tagName.toLowerCase() === 'table';
    const rows = isTable ? [...t.querySelectorAll('tr')] : [...t.querySelectorAll('[role=row]')];
    const cellsOf = (row) => isTable
      ? [...row.children].filter((c) => c.tagName === 'TD' || c.tagName === 'TH')
      : [...row.querySelectorAll(':scope > [role=gridcell], :scope > [role=cell], :scope > [role=columnheader], :scope > [role=rowheader]')];
    const headerRows = [];
    const dataRows = [];
    for (const row of rows) {
      const cs = cellsOf(row);
      if (!cs.length) continue;
      const inHead = isTable && !!row.closest('thead');
      const allHeader = cs.every((c) => c.tagName === 'TH' || role(c) === 'columnheader' || role(c) === 'rowheader');
      if (inHead || allHeader) headerRows.push(row); else dataRows.push(row);
    }
    let headers = [];
    if (headerRows.length) {
      headers = cellsOf(headerRows[headerRows.length - 1]).map((c) => norm(c.textContent));
    }
    const spans = [];
    let colTotal = 0;
    const body = dataRows.slice(0, maxRows).map((row, r) => {
      const all = cellsOf(row);
      colTotal = Math.max(colTotal, all.length);
      const cs = all.slice(0, maxColumns);
      for (let ci = 0; ci < cs.length; ci++) {
        const c = cs[ci];
        const colspan = isTable ? (c.colSpan || 1) : (parseInt(c.getAttribute('aria-colspan') || '1', 10) || 1);
        const rowspan = isTable ? (c.rowSpan || 1) : (parseInt(c.getAttribute('aria-rowspan') || '1', 10) || 1);
        if ((colspan > 1 || rowspan > 1) && spans.length < maxSpans) {
          spans.push({ row: r, col: ci, colspan: colspan, rowspan: rowspan });
        }
      }
      return cs.map((c) => norm(c.textContent));
    });
    const captionEl = t.querySelector('caption');
    return {
      caption: captionEl ? norm(captionEl.textContent) : '',
      headers: headers.slice(0, maxColumns),
      rows: body,
      span_info: spans,
      selector: cssPath(t),
      row_total: dataRows.length,
      col_total: colTotal
    };
  });
  return { tables: described, table_total: tables.length };
}`

// TableSpan marks one merged cell as a row/column offset with its spans. It
// lets a reader reconstruct the grid behind rows[][].
type TableSpan struct {
	// Row is the 0-based data-row index of the cell.
	Row int `json:"row"`
	// Col is the 0-based cell index within that row.
	Col int `json:"col"`
	// Colspan is the number of columns the cell covers (>= 1).
	Colspan int `json:"colspan"`
	// Rowspan is the number of rows the cell covers (>= 1).
	Rowspan int `json:"rowspan"`
}

// Table is one extracted table: its caption, headers, capped row grid, merged
// cell map and a CSS selector that addresses the container.
type Table struct {
	// Index is the 0-based table position in document order.
	Index int `json:"index"`
	// Caption is the table's caption text, or "" when it has none.
	Caption string `json:"caption"`
	// Headers is the header row's cell text, or [] when no header row exists.
	Headers []string `json:"headers"`
	// Rows is the data grid, one entry per row with one entry per cell.
	Rows [][]string `json:"rows"`
	// SpanInfo maps merged cells to their spans; [] when none are merged.
	SpanInfo []TableSpan `json:"span_info"`
	// Selector is a CSS selector addressing the table container.
	Selector string `json:"selector"`
}

// TablesResult is the JSON envelope returned by PageTables.
type TablesResult struct {
	// Tables are the extracted tables in document order.
	Tables []Table `json:"tables"`
	// Count is len(Tables).
	Count int `json:"count"`
	// Truncated is true when any table, row, column or span was dropped by a
	// cap, so the reader knows the grid is incomplete.
	Truncated bool `json:"truncated"`
	// TablesDropped is how many detected tables the table cap omitted.
	TablesDropped int `json:"tables_dropped"`
	// RowsDropped is how many data rows every reported table dropped at the row
	// cap, summed across tables.
	RowsDropped int `json:"rows_dropped"`
}

// rawTables is the probe's wire shape; it carries true totals alongside the
// safety-capped collections.
type rawTables struct {
	Tables     []rawTable `json:"tables"`
	TableTotal int        `json:"table_total"`
}

// rawTable is one table as the probe reports it.
type rawTable struct {
	Caption  string     `json:"caption"`
	Headers  []string   `json:"headers"`
	Rows     [][]string `json:"rows"`
	SpanInfo []rawSpan  `json:"span_info"`
	Selector string     `json:"selector"`
	RowTotal int        `json:"row_total"`
	ColTotal int        `json:"col_total"`
}

// rawSpan is one merged-cell annotation as the probe reports it.
type rawSpan struct {
	Row     int `json:"row"`
	Col     int `json:"col"`
	Colspan int `json:"colspan"`
	Rowspan int `json:"rowspan"`
}

// PageTables returns every table, role="table" and role="grid" container as
// {"tables":[{index,caption,headers,rows,span_info,selector}],"count":N,
// "truncated":bool,"tables_dropped":N,"rows_dropped":N}. Caps are 20 tables, 500
// data rows and 50 columns per table; truncated reports any dropped structure
// and tables_dropped/rows_dropped count the tables and data rows the caps
// omitted. The aspect self-caps, so it bypasses max_bytes.
func PageTables(ctx context.Context, p *browsers.Page) (string, error) {
	ctx, done, err := beginPage(ctx, p, "tables")
	if err != nil {
		return "", err
	}
	defer done()
	if p.Rod == nil {
		return "", fmt.Errorf("page session is closed")
	}

	res, err := p.Rod.Context(ctx).Eval(
		tablesProbeJS,
		tableSafetyTables,
		tableSafetyRows,
		tableSafetyColumns,
		tableSafetySpans,
	)
	if err != nil {
		return "", fmt.Errorf("evaluate tables: %w", err)
	}

	var raw rawTables
	if err := res.Value.Unmarshal(&raw); err != nil {
		return "", fmt.Errorf("decode tables: %w", err)
	}

	b, err := json.Marshal(shapeTables(raw))
	if err != nil {
		return "", fmt.Errorf("encode tables: %w", err)
	}
	return string(b), nil
}

// shapeTables applies the table caps to the probe's raw collections and builds
// the wire envelope. Totals reported by the probe keep truncation accurate even
// when the probe's own safety bound stopped collection early, and the dropped
// table and row counts make the truncation quantitative.
func shapeTables(raw rawTables) TablesResult {
	total := raw.TableTotal
	if total < len(raw.Tables) {
		total = len(raw.Tables)
	}

	tables := raw.Tables
	truncated := false
	tablesDropped := 0
	if len(tables) > tableMaxTables {
		tables = tables[:tableMaxTables]
		truncated = true
	}
	if total > len(tables) {
		tablesDropped = total - len(tables)
		truncated = true
	}

	out := TablesResult{
		Tables:        make([]Table, 0, len(tables)),
		TablesDropped: tablesDropped,
		RowsDropped:   0,
	}
	for i, rt := range tables {
		t := Table{
			Index:    i,
			Caption:  rt.Caption,
			Headers:  make([]string, 0, len(rt.Headers)),
			Rows:     make([][]string, 0, len(rt.Rows)),
			SpanInfo: make([]TableSpan, 0, len(rt.SpanInfo)),
			Selector: rt.Selector,
		}

		headers := rt.Headers
		if len(headers) > tableMaxColumns {
			headers = headers[:tableMaxColumns]
			truncated = true
		}
		t.Headers = append(t.Headers, headers...)

		rowTotal := rt.RowTotal
		if rowTotal < len(rt.Rows) {
			rowTotal = len(rt.Rows)
		}
		rows := rt.Rows
		if len(rows) > tableMaxRows {
			rows = rows[:tableMaxRows]
			truncated = true
		}
		if rowTotal > len(rows) {
			out.RowsDropped += rowTotal - len(rows)
			truncated = true
		}
		for _, row := range rows {
			if len(row) > tableMaxColumns {
				row = row[:tableMaxColumns]
				truncated = true
			}
			t.Rows = append(t.Rows, append([]string{}, row...))
		}
		if rt.ColTotal > tableMaxColumns {
			truncated = true
		}

		spans := rt.SpanInfo
		if len(spans) > tableMaxSpans {
			spans = spans[:tableMaxSpans]
			truncated = true
		}
		for _, s := range spans {
			t.SpanInfo = append(t.SpanInfo, TableSpan{
				Row:     s.Row,
				Col:     s.Col,
				Colspan: s.Colspan,
				Rowspan: s.Rowspan,
			})
		}

		out.Tables = append(out.Tables, t)
	}
	out.Count = len(out.Tables)
	out.Truncated = truncated
	return out
}

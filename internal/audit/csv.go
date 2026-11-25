package audit

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// CSVColumns are the columns of an export, in order.
var CSVColumns = []string{
	"id", "at", "actor_kind", "actor", "token", "door", "address", "action", "operation",
	"resource_kind", "resource_id", "resource_name", "project", "environment", "changes",
	"record_kind", "record_id",
}

// CSVWriter writes events as CSV, one row each, as they come. The header is written before the
// first row (or by Close, when there are none).
type CSVWriter struct {
	w       *csv.Writer
	started bool
}

// NewCSVWriter returns a writer on w.
func NewCSVWriter(w io.Writer) *CSVWriter { return &CSVWriter{w: csv.NewWriter(w)} }

func (c *CSVWriter) header() error {
	if c.started {
		return nil
	}
	c.started = true
	return c.w.Write(CSVColumns)
}

// Write adds the row of e.
func (c *CSVWriter) Write(e Event) error {
	if err := c.header(); err != nil {
		return err
	}
	var record Record
	if e.Record != nil {
		record = *e.Record
	}
	row := []string{
		strconv.FormatInt(e.ID, 10),
		e.At.UTC().Format(time.RFC3339Nano),
		string(e.Actor.Kind), e.Actor.Email, e.Actor.Token,
		string(e.Door), e.Address, string(e.Action), e.Operation,
		string(e.Resource.Kind), e.Resource.ID, e.Resource.Name, e.Resource.Project, e.Resource.Environment,
		ChangesText(e.Changes),
		string(record.Kind), record.ID,
	}
	for i := range row {
		row[i] = SafeCell(row[i])
	}
	return c.w.Write(row)
}

// Close writes the header if no row did, flushes, and returns the first error the writer had.
func (c *CSVWriter) Close() error {
	if err := c.header(); err != nil {
		return err
	}
	c.w.Flush()
	return c.w.Error()
}

// SafeCell keeps a spreadsheet from running a cell as a formula: a value that starts with =, +, -,
// @, a tab or a carriage return gets a leading apostrophe.
func SafeCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// ChangesText is the changes as one cell: "path op from → to" for each, joined by "; ", with the
// side a change doesn't have left out ("path added → to", "path removed from"). A change with no
// values (a secret's) reads "path op".
func ChangesText(changes []Change) string {
	parts := make([]string, 0, len(changes))
	for _, ch := range changes {
		p := fmt.Sprintf("%s %s", ch.Path, ch.Op)
		if ch.From != "" {
			p += " " + ch.From
		}
		if ch.To != "" {
			p += " → " + ch.To
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

package store

import (
	"fmt"
	"strings"

	"github.com/laterna-project/laterna/internal/domain"
)

// query builds an SQL query piece by piece, for lists whose filters and order vary. Values always
// go through "?" parameters, never into the text: each piece declares its own, and a number of "?"
// that differs from the number of values is a programming error (it panics, which tests catch).
type query struct {
	sb    strings.Builder
	args  []any
	conds []string
	cargs []any
	// profile is the profile of a card query (cardQuery), for what gets joined afterwards (reading
	// progress).
	profile domain.ID
}

// add appends SQL text and its arguments.
func (b *query) add(sql string, args ...any) {
	checkArgs(sql, args)
	b.sb.WriteString(sql)
	b.args = append(b.args, args...)
}

// where adds a condition, joined to the others with AND. Conditions are written by flushWhere at
// the right place in the query.
func (b *query) where(cond string, args ...any) *query {
	checkArgs(cond, args)
	b.conds = append(b.conds, cond)
	b.cargs = append(b.cargs, args...)
	return b
}

// flushWhere writes "WHERE ..." with the conditions gathered so far (nothing if there are none).
func (b *query) flushWhere() *query {
	if len(b.conds) > 0 {
		b.sb.WriteString(" WHERE ")
		b.sb.WriteString(strings.Join(b.conds, " AND "))
		b.args = append(b.args, b.cargs...)
		b.conds, b.cargs = nil, nil
	}
	return b
}

func (b *query) String() string { return b.sb.String() }

func checkArgs(sql string, args []any) {
	if n := strings.Count(sql, "?"); n != len(args) {
		panic(fmt.Sprintf("store: %d parameters for %d values in %q", n, len(args), sql))
	}
}

// placeholders returns "?, ?, ?" for n values.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

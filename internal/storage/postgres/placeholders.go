package postgres

import (
	"strconv"
	"strings"
)

// Placeholders builds only SQL parameter names; values remain bound parameters.
// SQLite compatibility repositories also accept this numbered syntax.
func Placeholders(start, count int) string {
	names := make([]string, count)
	for i := range names {
		names[i] = "$" + strconv.Itoa(start+i)
	}
	return strings.Join(names, ",")
}

package search

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Translate the existing FTS5 query surface into bound PGroonga conditions.
// User text never becomes SQL identifiers, operators, or SQL literals.
type queryToken struct {
	text   string
	quoted bool
}
type queryParser struct {
	tokens   []queryToken
	at       int
	args     []any
	keywords []string
}
type queryColumns [4]int

var allQueryColumns = queryColumns{20, 5, 1, 1}

func tokenizeQuery(s string) ([]queryToken, error) {
	var out []queryToken
	r := []rune(s)
	for i := 0; i < len(r); {
		if unicode.IsSpace(r[i]) {
			i++
			continue
		}
		if r[i] == '"' {
			i++
			var b strings.Builder
			closed := false
			for i < len(r) {
				if r[i] == '"' {
					i++
					if i < len(r) && r[i] == '"' {
						b.WriteRune('"')
						i++
						continue
					}
					closed = true
					break
				}
				b.WriteRune(r[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated search phrase")
			}
			out = append(out, queryToken{b.String(), true})
			continue
		}
		if strings.ContainsRune("():{}*+,-^", r[i]) {
			out = append(out, queryToken{text: string(r[i])})
			i++
			continue
		}
		start := i
		for i < len(r) && !unicode.IsSpace(r[i]) && !strings.ContainsRune("\"():{}*+,-^", r[i]) {
			if r[i] < 128 && !unicode.IsLetter(r[i]) && !unicode.IsDigit(r[i]) && r[i] != '_' && r[i] != 26 {
				return nil, fmt.Errorf("invalid search character %q", r[i])
			}
			i++
		}
		out = append(out, queryToken{text: string(r[start:i])})
	}
	return out, nil
}
func (p *queryParser) is(s string) bool {
	return p.at < len(p.tokens) && !p.tokens[p.at].quoted && p.tokens[p.at].text == s
}
func (p *queryParser) take(s string) bool {
	if p.is(s) {
		p.at++
		return true
	}
	return false
}
func (p *queryParser) require(s string) error {
	if !p.take(s) {
		return fmt.Errorf("expected %q in search query", s)
	}
	return nil
}
func groongaQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
func (p *queryParser) bind(s string) string {
	p.args = append(p.args, s)
	return fmt.Sprintf("$%d", len(p.args))
}
func (p *queryParser) condition(q string, cols queryColumns) string {
	arg := p.bind(q)
	return fmt.Sprintf(`ARRAY[title,headings,content,page_id] &@~ (%s,ARRAY[%d,%d,%d,%d],'search_pages_full_text')::pgroonga_full_text_search_condition`, arg, cols[0], cols[1], cols[2], cols[3])
}
func (p *queryParser) parseOr(cols queryColumns) (string, error) {
	left, err := p.parseAnd(cols)
	if err != nil {
		return "", err
	}
	for p.take("OR") {
		right, err := p.parseAnd(cols)
		if err != nil {
			return "", err
		}
		left = "(" + left + " OR " + right + ")"
	}
	return left, nil
}
func (p *queryParser) parseAnd(cols queryColumns) (string, error) {
	left, err := p.parseNot(cols)
	if err != nil {
		return "", err
	}
	for p.take("AND") {
		right, err := p.parseNot(cols)
		if err != nil {
			return "", err
		}
		left = "(" + left + " AND " + right + ")"
	}
	return left, nil
}
func (p *queryParser) parseNot(cols queryColumns) (string, error) {
	left, err := p.parseImplicit(cols)
	if err != nil {
		return "", err
	}
	for p.take("NOT") {
		right, err := p.parseImplicit(cols)
		if err != nil {
			return "", err
		}
		left = "(" + left + " AND NOT (" + right + "))"
	}
	return left, nil
}
func (p *queryParser) parseImplicit(cols queryColumns) (string, error) {
	left, err := p.atom(cols)
	if err != nil {
		return "", err
	}
	for p.at < len(p.tokens) && !p.is(")") && !p.is("AND") && !p.is("OR") && !p.is("NOT") {
		right, err := p.atom(cols)
		if err != nil {
			return "", err
		}
		left = "(" + left + " AND " + right + ")"
	}
	return left, nil
}
func (p *queryParser) columnFilter(cols queryColumns) (queryColumns, bool, error) {
	start := p.at
	exclude := p.take("-")
	set := p.take("{")
	if !set && !(p.at+1 < len(p.tokens) && p.tokens[p.at+1].text == ":") {
		p.at = start
		return cols, false, nil
	}
	var chosen queryColumns
	for {
		if p.at >= len(p.tokens) {
			return cols, false, fmt.Errorf("missing search column")
		}
		name := strings.ToLower(p.tokens[p.at].text)
		p.at++
		switch name {
		case "title":
			chosen[0] = 20
		case "headings":
			chosen[1] = 5
		case "content":
			chosen[2] = 1
		case "pageid":
			chosen[3] = 1
		case "path", "filepath", "kind":
		default:
			return cols, false, fmt.Errorf("unknown search column %q", name)
		}
		if !set || p.take("}") {
			break
		}
	}
	if err := p.require(":"); err != nil {
		return cols, false, err
	}
	for i := range cols {
		if (chosen[i] == 0 && !exclude) || (chosen[i] != 0 && exclude) {
			cols[i] = 0
		}
	}
	return cols, true, nil
}
func (p *queryParser) phrase() (string, string, error) {
	if p.at >= len(p.tokens) {
		return "", "", fmt.Errorf("missing search term")
	}
	tok := p.tokens[p.at]
	if !tok.quoted && strings.ContainsAny(tok.text, "():{}*+,-^") {
		return "", "", fmt.Errorf("unexpected search token %q", tok.text)
	}
	if !tok.quoted && (tok.text == "AND" || tok.text == "OR" || tok.text == "NOT") {
		return "", "", fmt.Errorf("missing operand before %s", tok.text)
	}
	p.at++
	text := tok.text
	prefix := p.take("*")
	for p.take("+") {
		_, next, err := p.phrase()
		if err != nil {
			return "", "", err
		}
		// Concatenation is a phrase in FTS5. Preserve text, not a boolean AND.
		text += " " + next
	}
	q := groongaQuote(text)
	if prefix {
		// Groonga prefix search takes a single index token. N-gram/mixed-script
		// phrases instead use normal tokenization so all Japanese tokens match.
		mixed := strings.ContainsFunc(text, func(r rune) bool { return r > 127 }) || (strings.ContainsFunc(text, unicode.IsLetter) && strings.ContainsFunc(text, unicode.IsDigit))
		if !mixed {
			q = text + "*"
		}
	}
	return q, text, nil
}
func (p *queryParser) atom(cols queryColumns) (string, error) {
	filtered, yes, err := p.columnFilter(cols)
	if err != nil {
		return "", err
	}
	if yes {
		return p.atom(filtered)
	}
	if p.take("(") {
		expr, err := p.parseOr(cols)
		if err != nil {
			return "", err
		}
		return expr, p.require(")")
	}
	if p.is("NEAR") && p.at+1 < len(p.tokens) && p.tokens[p.at+1].text == "(" {
		p.at += 2
		var terms []string
		for p.at < len(p.tokens) && !p.is(",") && !p.is(")") {
			_, text, err := p.phrase()
			if err != nil {
				return "", err
			}
			terms = append(terms, groongaQuote(text))
			p.keywords = append(p.keywords, text)
		}
		if len(terms) == 0 {
			return "", fmt.Errorf("empty NEAR search")
		}
		distance := 10
		if p.take(",") {
			if p.at >= len(p.tokens) {
				return "", fmt.Errorf("missing NEAR distance")
			}
			n, err := strconv.Atoi(p.tokens[p.at].text)
			if err != nil || n < 0 {
				return "", fmt.Errorf("invalid NEAR distance")
			}
			distance = n
			p.at++
		}
		if err := p.require(")"); err != nil {
			return "", err
		}
		return p.condition(fmt.Sprintf("*NP%d", distance)+groongaQuote(strings.Join(terms, " ")), cols), nil
	}
	anchored := p.take("^")
	q, text, err := p.phrase()
	if err != nil {
		return "", err
	}
	p.keywords = append(p.keywords, text)
	condition := p.condition(q, cols)
	if anchored {
		arg := p.bind(text)
		fields := []string{"title", "headings", "content", "page_id"}
		var starts []string
		for i, field := range fields {
			if cols[i] != 0 {
				starts = append(starts, `regexp_replace(`+field+`,'^[^[:alnum:]_/#.+-]*','','') &^ `+arg)
			}
		}
		if len(starts) == 0 {
			return "FALSE", nil
		}
		condition = "(" + condition + " AND (" + strings.Join(starts, " OR ") + "))"
	}
	return condition, nil
}
func compilePostgresQuery(query string) (string, []any, error) {
	tokens, err := tokenizeQuery(buildFuzzyQuery(query))
	if err != nil {
		return "", nil, err
	}
	// $1 is also used for snippets, and explicitly typed in the WHERE clause.
	p := queryParser{tokens: tokens, args: []any{""}}
	expr, err := p.parseOr(allQueryColumns)
	if err != nil {
		return "", nil, err
	}
	if p.at != len(tokens) {
		return "", nil, fmt.Errorf("unexpected search token %q", tokens[p.at].text)
	}
	quoted := make([]string, len(p.keywords))
	for i, k := range p.keywords {
		quoted[i] = groongaQuote(k)
	}
	p.args[0] = strings.Join(quoted, " ")
	return "($1::text IS NOT NULL AND (" + expr + "))", p.args, nil
}

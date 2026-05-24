package main

import "strings"

// scopePlanCSS rewrites a plan stylesheet so every rule applies only inside the
// given scope container (e.g. ".plan-detail-body") instead of leaking to the
// dashboard shell or being overridden by it. This lets the SPA embed render the
// plan at full fidelity using the SAME stylesheet as the standalone page —
// no rules are stripped, only re-scoped.
//
// Rewriting strategy per selector:
//   - ":root"                 -> scope            (custom properties resolve on the container)
//   - "html" / "body"         -> scope            (document-level layout maps onto the container,
//     reproducing the standalone two-column nav+content grid inside the panel)
//   - "body.foo .bar"         -> "scope.foo .bar" (body-state compounds re-anchor to the container)
//   - "[data-theme=x]"        -> "[data-theme=x] scope"
//   - "[data-theme=x] .bar"   -> "[data-theme=x] scope .bar"
//   - "*,*::before,*::after"  -> "scope *,scope *::before,scope *::after"
//   - anything else ".foo h2" -> "scope .foo h2"
//
// At-rules:
//   - @media (...) { ... }     -> inner selectors are scoped, the query kept verbatim
//   - @keyframes / @font-face  -> left untouched (no selectors to scope)
//
// The function is deliberately tolerant: malformed input is passed through rather
// than dropped, so a CSS edit can never silently blank the embed.
func scopePlanCSS(css, scope string) string {
	var out strings.Builder
	scopeRuleBlocks(css, scope, &out)
	return out.String()
}

// scopeRuleBlocks walks a CSS body splitting it into top-level rules at the
// brace boundaries and dispatching each to the appropriate rewriter.
func scopeRuleBlocks(css, scope string, out *strings.Builder) {
	i := 0
	n := len(css)
	for i < n {
		// Find the start of the next rule's prelude (skip leading whitespace).
		braceOpen := indexAtDepthZero(css[i:], '{')
		if braceOpen < 0 {
			// Trailing content with no block (whitespace/comments) — keep it.
			out.WriteString(css[i:])
			return
		}
		braceOpen += i
		prelude := strings.TrimSpace(css[i:braceOpen])
		// Find the matching closing brace for this block.
		blockEnd := matchBrace(css, braceOpen)
		if blockEnd < 0 {
			// Unbalanced — emit the rest verbatim and stop.
			out.WriteString(css[i:])
			return
		}
		inner := css[braceOpen+1 : blockEnd]

		switch {
		case strings.HasPrefix(prelude, "@media"), strings.HasPrefix(prelude, "@supports"):
			// Recurse into the conditional group; keep the query verbatim.
			out.WriteString(prelude)
			out.WriteString("{")
			scopeRuleBlocks(inner, scope, out)
			out.WriteString("}\n")
		case strings.HasPrefix(prelude, "@"):
			// @keyframes, @font-face, @import, etc. — no selectors to scope.
			out.WriteString(prelude)
			out.WriteString("{")
			out.WriteString(inner)
			out.WriteString("}\n")
		default:
			out.WriteString(scopeSelectorList(prelude, scope))
			out.WriteString("{")
			out.WriteString(inner)
			out.WriteString("}\n")
		}
		i = blockEnd + 1
	}
}

// scopeSelectorList rewrites a comma-separated selector list, scoping each
// selector under the container.
func scopeSelectorList(selectorList, scope string) string {
	parts := strings.Split(selectorList, ",")
	for i, p := range parts {
		parts[i] = scopeSelector(strings.TrimSpace(p), scope)
	}
	return strings.Join(parts, ",")
}

// scopeSelector rewrites a single selector so it matches only inside scope.
func scopeSelector(sel, scope string) string {
	if sel == "" {
		return sel
	}
	switch {
	case sel == ":root", sel == "html", sel == "body":
		// Document-level selectors map directly onto the container.
		return scope
	case sel == "*" || sel == "*::before" || sel == "*::after" ||
		sel == "*:before" || sel == "*:after":
		// Universal reset: confine to descendants of the container.
		return scope + " " + sel
	case strings.HasPrefix(sel, "body.") || strings.HasPrefix(sel, "body:") ||
		strings.HasPrefix(sel, "body["):
		// body-state compound (e.g. "body.left-nav-collapsed .x") re-anchors
		// the leading body to the container.
		return scope + sel[len("body"):]
	case strings.HasPrefix(sel, "html."), strings.HasPrefix(sel, "html:"),
		strings.HasPrefix(sel, "html["):
		return scope + sel[len("html"):]
	case strings.HasPrefix(sel, "[data-theme"):
		// Theme attribute lives on the document element / a wrapper. Insert the
		// scope after the leading attribute token so the theme still gates, but
		// the rule is confined to plan content.
		//   "[data-theme=\"light\"]"        -> "[data-theme=\"light\"] scope"
		//   "[data-theme=\"light\"] .badge" -> "[data-theme=\"light\"] scope .badge"
		close := strings.IndexByte(sel, ']')
		if close < 0 {
			return scope + " " + sel
		}
		attr := sel[:close+1]
		rest := strings.TrimSpace(sel[close+1:])
		if rest == "" {
			return attr + " " + scope
		}
		return attr + " " + scope + " " + rest
	default:
		return scope + " " + sel
	}
}

// indexAtDepthZero returns the index of the first occurrence of ch that is not
// nested inside braces or string literals, or -1.
func indexAtDepthZero(s string, ch byte) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\'' {
			i = skipString(s, i)
			continue
		}
		if c == ch && depth == 0 {
			return i
		}
		switch c {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return -1
}

// matchBrace returns the index of the '}' matching the '{' at openIdx, or -1.
func matchBrace(s string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(s); i++ {
		switch s[i] {
		case '"', '\'':
			i = skipString(s, i)
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// skipString advances past a string literal starting at the quote at idx and
// returns the index of the closing quote (so a for-loop's i++ lands after it).
func skipString(s string, idx int) int {
	quote := s[idx]
	for i := idx + 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			return i
		}
	}
	return len(s) - 1
}

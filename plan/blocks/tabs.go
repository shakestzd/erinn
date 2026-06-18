package blocks

import (
	"hash/fnv"
	"io"
	"strconv"
)

// maxCSSTabs is the number of tabs the pure-CSS toggle styling supports (the page
// CSS enumerates :nth-of-type rules up to this count). Extra tabs still render and
// their labels show, but only the first maxCSSTabs participate in CSS switching.
const maxCSSTabs = 8

// Tab is one labeled panel of a Tabs block.
type Tab struct {
	Label string
	Body  string
}

// Tabs renders a set of labeled panels as pure-CSS tabs — hidden radio inputs
// plus :checked sibling selectors — so tab switching works in a committed,
// JS-free standalone artifact as well as in the dashboard. The radio group name
// is derived from the tab labels so multiple Tabs blocks on one page don't
// collide.
type Tabs struct {
	Title string
	Tabs  []Tab
}

// Render writes the tabs block HTML to w.
func (t *Tabs) Render(w io.Writer) error {
	group := "tabs-" + t.groupID()
	type tabView struct {
		ID      string
		Label   string
		Body    string
		Checked bool
	}
	views := make([]tabView, 0, len(t.Tabs))
	for i, tb := range t.Tabs {
		views = append(views, tabView{
			ID:      group + "-" + strconv.Itoa(i),
			Label:   tb.Label,
			Body:    tb.Body,
			Checked: i == 0,
		})
	}
	return blockTmpl.ExecuteTemplate(w, "tabs", struct {
		Title string
		Group string
		Tabs  []tabView
	}{Title: t.Title, Group: group, Tabs: views})
}

// groupID derives a stable, collision-resistant id from the tab labels so two
// Tabs blocks on the same page get distinct radio groups.
func (t *Tabs) groupID() string {
	h := fnv.New32a()
	for _, tb := range t.Tabs {
		_, _ = h.Write([]byte(tb.Label))
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(uint64(h.Sum32()), 36)
}

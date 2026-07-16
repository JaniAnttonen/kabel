package main

import "sort"

// listItem is one row of the channel list: either a section header or a
// reference into the merged channel slice.
type listItem struct {
	header  string // non-empty: unselectable section header
	chanIdx int
}

const undefinedCategory = "Undefined"

// arrangeChannels merges the local (Fritz!Box) and public channels into
// display order: local channels pinned on top, then public channels grouped
// by category (alphabetical, Undefined last) with home-country channels
// first within each group. Headers are omitted when there is only a single
// unnamed group (e.g. a plain Fritz!Box-only playlist).
func arrangeChannels(local, public []Channel, home string) ([]Channel, []listItem) {
	channels := make([]Channel, 0, len(local)+len(public))
	items := make([]listItem, 0, len(local)+len(public)+32)

	add := func(c Channel) {
		items = append(items, listItem{chanIdx: len(channels)})
		channels = append(channels, c)
	}

	if len(local) > 0 {
		items = append(items, listItem{header: "Local — Fritz!Box", chanIdx: -1})
		for _, c := range local {
			add(c)
		}
	}

	groups := map[string][]Channel{}
	var names []string
	for _, c := range public {
		cat := c.Category
		if cat == "" {
			cat = undefinedCategory
		}
		if _, ok := groups[cat]; !ok {
			names = append(names, cat)
		}
		groups[cat] = append(groups[cat], c)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == undefinedCategory || names[j] == undefinedCategory {
			return names[j] == undefinedCategory
		}
		return names[i] < names[j]
	})

	showHeaders := len(local) > 0 || len(names) > 1 || (len(names) == 1 && names[0] != undefinedCategory)
	for _, name := range names {
		group := groups[name]
		if home != "" {
			sort.SliceStable(group, func(i, j int) bool {
				return group[i].Country == home && group[j].Country != home
			})
		}
		if showHeaders {
			items = append(items, listItem{header: name, chanIdx: -1})
		}
		for _, c := range group {
			add(c)
		}
	}
	return channels, items
}

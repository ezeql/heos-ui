// Package webui serves the heoscli web page and its JSON API, backed by
// goheos instead of pyheos. The frontend is byte-identical to the Python
// one: same index.html, same endpoints, same response shapes.
package webui

import (
	"sort"

	"github.com/ezeql/goheos"
)

// playerView is a snapshot of one player. The handlers work on these rather
// than live goheos objects so target resolution and state assembly are plain
// functions, testable without a speaker.
type playerView struct {
	ID      int
	Name    string
	Model   string
	IP      string
	State   string
	Volume  int
	Muted   bool
	Playing bool
}

type groupView struct {
	ID      int
	Name    string
	Leader  int
	Members []int
	Volume  int
	Muted   bool
}

func snapshotPlayers(players map[int]*goheos.Player) []playerView {
	views := make([]playerView, 0, len(players))
	for _, p := range players {
		state := string(p.State())
		if state == "" {
			state = "unknown"
		}
		views = append(views, playerView{
			ID:      p.ID(),
			Name:    p.Name(),
			Model:   p.Model(),
			IP:      p.IPAddress(),
			State:   state,
			Volume:  p.Volume(),
			Muted:   p.IsMuted(),
			Playing: p.State() == goheos.PlayStatePlay,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}

func snapshotGroups(groups map[int]*goheos.Group) []groupView {
	views := make([]groupView, 0, len(groups))
	for _, g := range groups {
		views = append(views, groupView{
			ID:      g.ID(),
			Name:    g.Name(),
			Leader:  g.LeadPlayerID(),
			Members: g.MemberPlayerIDs(),
			Volume:  g.Volume(),
			Muted:   g.IsMuted(),
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}

func findPlayer(players []playerView, id int) (playerView, bool) {
	for _, p := range players {
		if p.ID == id {
			return p, true
		}
	}
	return playerView{}, false
}

func findGroup(groups []groupView, id int) (groupView, bool) {
	for _, g := range groups {
		if g.ID == id {
			return g, true
		}
	}
	return groupView{}, false
}

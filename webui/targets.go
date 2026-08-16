package webui

// Target resolution: from a name to a player id.
//
// Playing to a group is done by sending the command to the leader's pid:
// there is no group/play_stream and the group behaves as a single zone. A
// group's gid is always its leader's pid.

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// target is where the music will play.
type target struct {
	playerID    int
	label       string
	groupID     int
	isGroup     bool
	memberNames []string
}

func (t target) describe() string {
	if t.isGroup && len(t.memberNames) > 0 {
		return fmt.Sprintf("%s (%s)", t.label, strings.Join(t.memberNames, ", "))
	}
	return t.label
}

// matchPlayer looks up by exact pid or by name, case-insensitively, falling
// back to a partial match when it is unambiguous.
func matchPlayer(players []playerView, spec string) (playerView, bool) {
	if id, err := strconv.Atoi(strings.TrimSpace(spec)); err == nil {
		if p, ok := findPlayer(players, id); ok {
			return p, true
		}
	}
	wanted := strings.ToLower(strings.TrimSpace(spec))
	for _, p := range players {
		if strings.ToLower(p.Name) == wanted {
			return p, true
		}
	}
	var partial []playerView
	for _, p := range players {
		if strings.Contains(strings.ToLower(p.Name), wanted) {
			partial = append(partial, p)
		}
	}
	if len(partial) == 1 {
		return partial[0], true
	}
	return playerView{}, false
}

// biggest returns the group with the most speakers: the best bet for "play
// everywhere".
func biggest(groups []groupView) groupView {
	best := groups[0]
	for _, g := range groups[1:] {
		if len(g.Members) > len(best.Members) {
			best = g
		}
	}
	return best
}

func groupTarget(group groupView, players []playerView) target {
	var names []string
	for _, pid := range append([]int{group.Leader}, group.Members...) {
		if p, ok := findPlayer(players, pid); ok {
			names = append(names, p.Name)
		}
	}
	return target{
		playerID:    group.Leader,
		label:       group.Name,
		groupID:     group.ID,
		isGroup:     true,
		memberNames: names,
	}
}

func knownNames(players []playerView) string {
	names := make([]string, len(players))
	for i, p := range players {
		names[i] = p.Name
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// resolveTarget translates what the user asked for into the pid to send
// commands to. With nothing specified: the active group if there is one
// (which is what most people want for multiroom), otherwise the only
// available speaker.
func resolveTarget(players []playerView, groups []groupView, spec string) (target, error) {
	if len(players) == 0 {
		return target{}, errors.New("the HEOS system reported no speakers")
	}

	if spec != "" {
		wanted := strings.ToLower(strings.TrimSpace(spec))
		switch wanted {
		case "all", "todos", "group", "grupo":
			if len(groups) == 0 {
				return target{}, errors.New("there is no active group")
			}
			return groupTarget(biggest(groups), players), nil
		}
		for _, group := range groups {
			if strings.ToLower(group.Name) == wanted {
				return groupTarget(group, players), nil
			}
		}
		player, ok := matchPlayer(players, spec)
		if !ok {
			return target{}, fmt.Errorf(
				"no speaker named '%s' was found. Available: %s", spec, knownNames(players))
		}
		return target{playerID: player.ID, label: player.Name}, nil
	}

	if len(groups) > 0 {
		return groupTarget(biggest(groups), players), nil
	}
	if len(players) == 1 {
		return target{playerID: players[0].ID, label: players[0].Name}, nil
	}
	return target{}, fmt.Errorf(
		"there are several speakers and none are grouped. Pick one. Available: %s",
		knownNames(players))
}

// pickPlayer chooses whose playback to describe on the page. Normally the
// resolved target; when there is none — several speakers and no group — the
// page still has to show something, so prefer whichever speaker is actually
// playing and fall back to the first by name.
func pickPlayer(players []playerView, resolved *target) (playerView, bool) {
	if resolved != nil {
		if p, ok := findPlayer(players, resolved.playerID); ok {
			return p, true
		}
	}
	var candidates []playerView
	for _, p := range players {
		if p.Playing {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		candidates = players
	}
	if len(candidates) == 0 {
		return playerView{}, false
	}
	best := candidates[0]
	for _, p := range candidates[1:] {
		if p.Name < best.Name {
			best = p
		}
	}
	return best, true
}

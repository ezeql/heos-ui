package webui

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ezeql/goheos"

	"github.com/ezeql/heoscli-go/position"
)

// state answers the poll the page lives on: players, groups, the acting
// target and what is playing. Shapes mirror the Python webui field for
// field.
func (s *Server) state(r *http.Request) (any, error) {
	ctx := r.Context()
	players, groups, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}

	groupNameOf := map[int]string{}
	isLeader := map[int]bool{}
	for _, g := range groups {
		groupNameOf[g.Leader] = g.Name
		isLeader[g.Leader] = true
		for _, pid := range g.Members {
			groupNameOf[pid] = g.Name
		}
	}

	playerList := make([]map[string]any, 0, len(players))
	for _, p := range players {
		var groupName any
		if name, ok := groupNameOf[p.ID]; ok {
			groupName = name
		}
		playerList = append(playerList, map[string]any{
			"id": p.ID, "name": p.Name, "model": p.Model, "ip": p.IP,
			"state": p.State, "volume": p.Volume, "muted": p.Muted,
			"group": groupName, "is_leader": isLeader[p.ID],
		})
	}
	groupList := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		groupList = append(groupList, map[string]any{
			"id": g.ID, "name": g.Name, "leader": g.Leader,
			"members": g.Members, "volume": g.Volume,
		})
	}
	payload := map[string]any{"players": playerList, "groups": groupList}

	// Several speakers and no group: no sensible default to act on. That is
	// no reason to refuse the whole page — the speaker list is exactly what
	// someone needs in order to fix it.
	pinned := s.pinnedTarget()
	var resolved *target
	if t, err := resolveTarget(players, groups, pinned); err == nil {
		resolved = &t
	}

	if resolved == nil {
		payload["target"] = nil
	} else {
		// The master control acts on the group when there is one, so its own
		// level and mute state are what the slider has to show.
		var volume any
		muted := false
		if resolved.isGroup {
			if g, ok := findGroup(groups, resolved.groupID); ok {
				volume, muted = g.Volume, g.Muted
			}
		} else if p, ok := findPlayer(players, resolved.playerID); ok {
			volume, muted = p.Volume, p.Muted
		}
		payload["target"] = map[string]any{
			"id": resolved.playerID, "label": resolved.describe(),
			"is_group": resolved.isGroup, "volume": volume, "muted": muted,
			"pinned": pinned != "",
		}
	}

	shown, ok := pickPlayer(players, resolved)
	if !ok {
		payload["now"] = nil
		return payload, nil
	}
	payload["now"] = s.nowPayload(shown, players)
	return payload, nil
}

// nowPayload describes what the shown player is playing, with the clock
// filling in for streams the firmware cannot time.
func (s *Server) nowPayload(shown playerView, players []playerView) map[string]any {
	livePlayers := s.heos.Players()
	live := livePlayers[shown.ID]
	var media goheos.NowPlayingMedia
	if live != nil {
		media = live.NowPlaying()
	}

	// A URL stream repeats the same literal in every field; collapse it so
	// the page does not show "Url Stream" three times over.
	unique := map[string]bool{}
	for _, f := range []string{media.Song, media.Artist, media.Album} {
		if f != "" {
			unique[f] = true
		}
	}

	var positionMS, durationMS any
	if media.CurrentPosition > 0 {
		positionMS = media.CurrentPosition
	}
	if media.Duration > 0 {
		durationMS = media.Duration
	}
	estimated := false

	s.mu.Lock()
	title, artist, image := s.title, s.artist, s.image
	s.mu.Unlock()
	if durationMS == nil {
		// The firmware reports nothing for URL streams, so fall back to the
		// clock we started when playback began. Milliseconds, like HEOS.
		playing := false
		for _, p := range players {
			if p.Playing {
				playing = true
				break
			}
		}
		if ms, ok := s.clockReading(playing); ok {
			positionMS = ms
			durationMS = nil
			estimated = true
		}
	}

	return map[string]any{
		"song":          nullable(media.Song),
		"artist":        nullable(media.Artist),
		"album":         nullable(media.Album),
		"image":         nullable(media.ImageURL),
		"position":      positionMS,
		"duration":      durationMS,
		"estimated":     estimated,
		"is_url_stream": len(unique) == 1 && media.Song != "",
		"known_title":   nullable(title),
		"known_artist":  nullable(artist),
		"known_image":   nullable(image),
		// The DIDL quality parse is not ported yet; the page treats null as
		// "no line", same as a speaker that does not answer.
		"quality": nil,
	}
}

// clockReading syncs our clock with the speaker's state and reads it, in
// milliseconds; ok is false when we started nothing. It holds s.mu
// throughout: every open tab polls /api/state on its own goroutine, and
// seek re-anchors the same clock.
func (s *Server) clockReading(playing bool) (ms int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clock == nil {
		return 0, false
	}
	s.clock.Sync(playing)
	return int(s.clock.Position() * 1000), true
}

// anchorClock moves our clock to a position the speaker confirmed.
func (s *Server) anchorClock(seconds float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clock != nil {
		s.clock.Anchor(seconds)
	}
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// targetHandler chooses which speaker the controls act on.
func (s *Server) targetHandler(r *http.Request) (any, error) {
	data := readJSON(r)
	spec := stringField(data, "player")

	if spec == "" || spec == "auto" {
		// Back to working it out per request: the right behaviour again as
		// soon as the speakers are grouped.
		s.mu.Lock()
		s.target = ""
		s.mu.Unlock()
		return map[string]any{"target": nil}, nil
	}

	players, groups, err := s.snapshot(r.Context())
	if err != nil {
		return nil, err
	}
	chosen, err := resolveTarget(players, groups, spec)
	if err != nil {
		return nil, badRequest("%s", err.Error())
	}
	s.mu.Lock()
	s.target = spec
	s.mu.Unlock()
	return map[string]any{"target": chosen.describe()}, nil
}

func (s *Server) play(r *http.Request) (any, error) {
	ctx := r.Context()
	data := readJSON(r)
	url := strings.TrimSpace(stringField(data, "url"))
	if url == "" {
		return nil, badRequest("a link is required")
	}
	players, groups, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveRequestTarget(data, players, groups)
	if err != nil {
		return nil, err
	}

	info, err := resolveSpec(ctx, url)
	if err != nil {
		return nil, badRequest("%s", err.Error())
	}
	if err := s.heos.PlayURL(ctx, resolved.playerID, info.streamURL); err != nil {
		return nil, badRequest("the speaker would not play it: %s", err.Error())
	}

	s.mu.Lock()
	s.clock = position.NewClock(info.duration)
	s.title, s.artist, s.image, s.spec = info.title, info.artist, info.image, url
	s.mu.Unlock()

	return map[string]any{
		"route":  "direct",
		"detail": "played as a direct URL",
		"title":  nullable(info.title),
		"artist": nullable(info.artist),
		"target": resolved.describe(),
		"tracks": []string{},
	}, nil
}

// identify is the acoustic track identification, deliberately not ported.
func (s *Server) identify(_ *http.Request) (any, error) {
	return nil, &apiError{
		status: http.StatusNotImplemented,
		reason: "identifying tracks by listening is not ported to this backend yet",
	}
}

func (s *Server) seek(r *http.Request) (any, error) {
	data := readJSON(r)
	ms, ok := numberField(data, "position")
	if !ok {
		return nil, badRequest("a position is required")
	}
	seconds := max(0, ms/1000)

	players, groups, err := s.snapshot(r.Context())
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveRequestTarget(data, players, groups)
	if err != nil {
		return nil, err
	}
	player, ok := findPlayer(players, resolved.playerID)
	if !ok || player.IP == "" {
		return nil, badRequest("unknown speaker address")
	}

	// Over UPnP rather than HEOS: the HEOS protocol has no seek at all.
	if !position.Seek(r.Context(), player.IP, seconds, 5*time.Second) {
		return nil, &apiError{
			status: http.StatusConflict,
			reason: "this speaker would not seek in what it is playing",
		}
	}
	// Our own clock has to follow, or the page would snap back.
	s.anchorClock(seconds)
	return map[string]any{"position": int(seconds * 1000)}, nil
}

func (s *Server) transport(r *http.Request) (any, error) {
	ctx := r.Context()
	data := readJSON(r)
	players, groups, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveRequestTarget(data, players, groups)
	if err != nil {
		return nil, err
	}

	switch action := stringField(data, "action"); action {
	case "play":
		err = s.heos.SetPlayState(ctx, resolved.playerID, goheos.PlayStatePlay)
	case "pause":
		err = s.heos.SetPlayState(ctx, resolved.playerID, goheos.PlayStatePause)
	case "stop":
		err = s.heos.SetPlayState(ctx, resolved.playerID, goheos.PlayStateStop)
	case "next":
		err = s.heos.PlayNext(ctx, resolved.playerID)
	case "prev":
		err = s.heos.PlayPrevious(ctx, resolved.playerID)
	default:
		return nil, badRequest("unknown action: %s", action)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (s *Server) volume(r *http.Request) (any, error) {
	ctx := r.Context()
	data := readJSON(r)
	level, ok := numberField(data, "level")
	if !ok {
		return nil, badRequest("level is required")
	}
	clamped := min(100, max(0, int(level)))

	players, groups, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveRequestTarget(data, players, groups)
	if err != nil {
		return nil, err
	}

	// On a group this keeps the relative balance between speakers.
	if resolved.isGroup && stringField(data, "player") == "" {
		err = s.heos.SetGroupVolume(ctx, resolved.groupID, clamped)
	} else {
		err = s.heos.SetVolume(ctx, resolved.playerID, clamped)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "level": clamped}, nil
}

// mute mutes, unmutes or toggles. Without a speaker named, the whole group.
func (s *Server) mute(r *http.Request) (any, error) {
	ctx := r.Context()
	data := readJSON(r)
	players, groups, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveRequestTarget(data, players, groups)
	if err != nil {
		return nil, err
	}
	wholeGroup := resolved.isGroup && stringField(data, "player") == ""

	state := strings.ToLower(stringField(data, "state"))
	if state == "" {
		state = "toggle"
	}
	switch state {
	case "toggle":
		if wholeGroup {
			err = s.heos.GroupToggleMute(ctx, resolved.groupID)
		} else {
			err = s.heos.ToggleMute(ctx, resolved.playerID)
		}
	case "on", "off":
		wanted := state == "on"
		if wholeGroup {
			err = s.heos.SetGroupMute(ctx, resolved.groupID, wanted)
		} else {
			err = s.heos.SetMute(ctx, resolved.playerID, wanted)
		}
	default:
		return nil, badRequest("unknown mute state: %s", state)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (s *Server) group(r *http.Request) (any, error) {
	ctx := r.Context()
	data := readJSON(r)
	switch action := stringField(data, "action"); action {
	case "ungroup":
		groups, err := s.heos.GetGroups(ctx)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if err := s.heos.RemoveGroup(ctx, g.ID()); err != nil {
				return nil, err
			}
		}
	case "create":
		raw, _ := data["players"].([]any)
		ids := make([]int, 0, len(raw))
		for _, entry := range raw {
			if id, ok := entry.(float64); ok {
				ids = append(ids, int(id))
			}
		}
		if len(ids) < 2 {
			return nil, badRequest("a group needs at least two speakers")
		}
		if err := s.heos.CreateGroup(ctx, ids[0], ids[1:]); err != nil {
			return nil, err
		}
	default:
		return nil, badRequest("unknown action: %s", action)
	}
	return map[string]any{"ok": true}, nil
}

// -- account ---------------------------------------------------------------

// accountState is the session status plus which sources it actually enabled.
// The sources are the point: signing in is only useful insofar as it turns
// TIDAL or SoundCloud from available:false into something playable.
func (s *Server) accountState(r *http.Request) map[string]any {
	ctx := r.Context()
	username := ""
	if name, signedIn, err := s.heos.CheckAccount(ctx); err == nil && signedIn {
		username = name
	}

	sources := []map[string]any{}
	if err := s.heos.RefreshMusicSources(ctx); err == nil {
		for id, source := range s.heos.MusicSources() {
			if !cliSearchable[id] {
				continue
			}
			sources = append(sources, map[string]any{
				"id": id, "name": source.Name,
				"available": source.Available,
				"account":   nullable(source.ServiceUsername),
			})
		}
	}
	for i := range sources {
		for j := i + 1; j < len(sources); j++ {
			if sources[j]["name"].(string) < sources[i]["name"].(string) {
				sources[i], sources[j] = sources[j], sources[i]
			}
		}
	}

	return map[string]any{
		"signed_in": username != "",
		"username":  nullable(username),
		"sources":   sources,
	}
}

func (s *Server) account(r *http.Request) (any, error) {
	return s.accountState(r), nil
}

func (s *Server) accountSignin(r *http.Request) (any, error) {
	data := readJSON(r)
	username := strings.TrimSpace(stringField(data, "username"))
	password := stringField(data, "password")
	if username == "" || password == "" {
		return nil, badRequest("email and password are both required")
	}
	if err := s.heos.SignIn(r.Context(), username, password); err != nil {
		if errors.Is(err, goheos.ErrAuthenticationFailure) {
			return nil, &apiError{
				status: http.StatusUnauthorized,
				reason: "HEOS rejected those credentials. Use the email and password of your HEOS account — the same as the app — not the streaming service's.",
			}
		}
		return nil, badRequest("could not sign in: %s", err.Error())
	}
	return s.accountState(r), nil
}

func (s *Server) accountSignout(r *http.Request) (any, error) {
	if err := s.heos.SignOut(r.Context()); err != nil {
		return nil, badRequest("%s", err.Error())
	}
	return s.accountState(r), nil
}

// -- playlists -------------------------------------------------------------

func playlistJSON(playlist *Playlist) map[string]any {
	items := make([]map[string]any, 0, len(playlist.Items))
	for _, item := range playlist.Items {
		items = append(items, map[string]any{
			"url": item.URL, "title": item.Title, "artist": item.Artist,
			"duration": item.Duration, "image": item.Image,
		})
	}
	return map[string]any{
		"name": playlist.Name, "duration": playlist.TotalDuration(),
		"image": playlist.Image(), "items": items,
	}
}

func (s *Server) playlists(_ *http.Request) (any, error) {
	all := s.library.All()
	payload := make([]map[string]any, len(all))
	for i, playlist := range all {
		payload[i] = playlistJSON(playlist)
	}
	return map[string]any{"playlists": payload}, nil
}

func (s *Server) playlistCreate(r *http.Request) (any, error) {
	playlist, err := s.library.Create(stringField(readJSON(r), "name"))
	if err != nil {
		return nil, badRequest("%s", err.Error())
	}
	return playlistJSON(playlist), nil
}

func (s *Server) playlistDelete(r *http.Request) (any, error) {
	if err := s.library.Delete(stringField(readJSON(r), "name")); err != nil {
		return nil, badRequest("%s", err.Error())
	}
	return map[string]any{"ok": true}, nil
}

// playlistAdd resolves the link once so the entry carries real metadata.
func (s *Server) playlistAdd(r *http.Request) (any, error) {
	data := readJSON(r)
	name := stringField(data, "name")
	url := strings.TrimSpace(stringField(data, "url"))
	if url == "" {
		return nil, badRequest("a link is required")
	}
	if _, err := s.library.Get(name); err != nil {
		return nil, badRequest("%s", err.Error())
	}
	item := PlaylistItem{URL: url, Title: url}
	if info, err := resolveSpec(r.Context(), url); err == nil {
		item.Title = info.title
		if info.artist != "" {
			item.Artist = &info.artist
		}
		if info.duration > 0 {
			item.Duration = &info.duration
		}
		if info.image != "" {
			item.Image = &info.image
		}
	}
	playlist, err := s.library.Add(name, item)
	if err != nil {
		return nil, badRequest("%s", err.Error())
	}
	return playlistJSON(playlist), nil
}

func (s *Server) playlistRemove(r *http.Request) (any, error) {
	data := readJSON(r)
	index, ok := numberField(data, "index")
	if !ok {
		index = -1
	}
	playlist, err := s.library.Remove(stringField(data, "name"), int(index))
	if err != nil {
		return nil, badRequest("%s", err.Error())
	}
	return playlistJSON(playlist), nil
}

func (s *Server) playlistMove(r *http.Request) (any, error) {
	data := readJSON(r)
	index, okIndex := numberField(data, "index")
	to, okTo := numberField(data, "to")
	if !okIndex || !okTo {
		return nil, badRequest("index and to are required")
	}
	playlist, err := s.library.Move(stringField(data, "name"), int(index), int(to))
	if err != nil {
		return nil, badRequest("%s", err.Error())
	}
	return playlistJSON(playlist), nil
}

// playlistPlay needs the proxy feeder, which is deliberately not ported: it
// plays the first track directly instead so the button still does something
// honest.
func (s *Server) playlistPlay(r *http.Request) (any, error) {
	ctx := r.Context()
	data := readJSON(r)
	playlist, err := s.library.Get(stringField(data, "name"))
	if err != nil {
		return nil, badRequest("%s", err.Error())
	}
	if len(playlist.Items) == 0 {
		return nil, badRequest("'%s' is empty", playlist.Name)
	}
	players, groups, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveRequestTarget(data, players, groups)
	if err != nil {
		return nil, err
	}

	first := playlist.Items[0]
	info, err := resolveSpec(ctx, first.URL)
	if err != nil {
		return nil, badRequest("%s", err.Error())
	}
	if err := s.heos.PlayURL(ctx, resolved.playerID, info.streamURL); err != nil {
		return nil, badRequest("the speaker would not play it: %s", err.Error())
	}
	s.mu.Lock()
	s.clock = position.NewClock(info.duration)
	s.title, s.artist, s.image, s.spec = info.title, info.artist, info.image, first.URL
	s.mu.Unlock()

	titles := make([]string, len(playlist.Items))
	for i, item := range playlist.Items {
		titles[i] = item.Title
	}
	return map[string]any{
		"route":  "direct",
		"detail": "queue feeding is not ported yet: playing the first track only",
		"title":  playlist.Name,
		"target": resolved.describe(),
		"tracks": titles,
	}, nil
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
)

type TrackMeta struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Artist        string `json:"artist"`
	Album         string `json:"album"`
	AlbumImageURL string `json:"album_image_url"`
	ArtID         string `json:"art_id"`
	IsPlaying     bool   `json:"is_playing"`
	ProgressMs    int    `json:"progress_ms"`
	DurationMs    int    `json:"duration_ms"`
}

type WindowSlot struct {
	Track    *TrackMeta `json:"track"`
	HasArt   bool       `json:"has_art"`
	Position string     `json:"position"`
}

type TrackWindow struct {
	Prev2   WindowSlot `json:"prev2"`
	Prev1   WindowSlot `json:"prev1"`
	Current WindowSlot `json:"current"`
	Next1   WindowSlot `json:"next1"`
	Next2   WindowSlot `json:"next2"`
}

type CacheState struct {
	mu sync.RWMutex

	// processed RGB565 artwork keyed by album image URL
	artCache map[string][]byte

	// metadata keyed by track ID
	trackCache map[string]*TrackMeta

	// playback history, oldest -> newest
	history []*TrackMeta

	// current track
	current *TrackMeta
}

var cacheState = &CacheState{
	artCache:   make(map[string][]byte),
	trackCache: make(map[string]*TrackMeta),
	history:    make([]*TrackMeta, 0, 32),
}

func parseSpotifyNowPlaying(r *http.Response) (*TrackMeta, error) {
	if r.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if r.StatusCode != http.StatusOK {
		return nil, errors.New("spotify returned non-200")
	}

	var raw struct {
		IsPlaying  bool `json:"is_playing"`
		ProgressMs int  `json:"progress_ms"`
		Item       *struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			DurationMs int    `json:"duration_ms"`
			Artists    []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Album struct {
				Name   string `json:"name"`
				Images []struct {
					URL string `json:"url"`
				} `json:"images"`
			} `json:"album"`
		} `json:"item"`
	}

	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, err
	}

	if raw.Item == nil {
		return nil, nil
	}

	var artistNames []string
	for _, a := range raw.Item.Artists {
		if a.Name != "" {
			artistNames = append(artistNames, a.Name)
		}
	}

	imageURL := ""
	if len(raw.Item.Album.Images) > 0 {
		imageURL = raw.Item.Album.Images[0].URL
	}

	artID := imageURL
	if artID == "" {
		artID = raw.Item.ID
	}

	return &TrackMeta{
		ID:            raw.Item.ID,
		Title:         raw.Item.Name,
		Artist:        strings.Join(artistNames, ", "),
		Album:         raw.Item.Album.Name,
		AlbumImageURL: imageURL,
		ArtID:         artID,
		IsPlaying:     raw.IsPlaying,
		ProgressMs:    raw.ProgressMs,
		DurationMs:    raw.Item.DurationMs,
	}, nil
}

func fetchSpotifyCurrentTrackCtx(ctx context.Context) (*TrackMeta, error) {
	accessToken, err := getValidAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nowPlayURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return parseSpotifyNowPlaying(resp)
}

func fetchSpotifyCurrentTrack(r *http.Request) (*TrackMeta, error) {
	return fetchSpotifyCurrentTrackCtx(r.Context())
}

func (c *CacheState) updateCurrentTrack(track *TrackMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if track == nil {
		c.current = nil
		return
	}

	// Same track: just refresh dynamic fields
	if c.current != nil && c.current.ID == track.ID {
		c.current.IsPlaying = track.IsPlaying
		c.current.ProgressMs = track.ProgressMs
		c.current.DurationMs = track.DurationMs
		c.trackCache[track.ID] = c.current
		return
	}

	// Track changed: push old current into history
	if c.current != nil {
		c.history = append(c.history, c.current)
		if len(c.history) > 50 {
			c.history = c.history[len(c.history)-50:]
		}
	}

	c.current = track
	c.trackCache[track.ID] = track
}

func (c *CacheState) ensureArtProcessed(track *TrackMeta) error {
	if track == nil || track.AlbumImageURL == "" {
		return nil
	}

	c.mu.RLock()
	_, ok := c.artCache[track.AlbumImageURL]
	c.mu.RUnlock()
	if ok {
		return nil
	}

	payload, err := processAlbumArt(track.AlbumImageURL)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.artCache[track.AlbumImageURL] = payload
	c.mu.Unlock()

	return nil
}

func (c *CacheState) getArt(track *TrackMeta) ([]byte, bool) {
	if track == nil || track.AlbumImageURL == "" {
		return nil, false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	data, ok := c.artCache[track.AlbumImageURL]
	return data, ok
}

func (c *CacheState) buildWindow() TrackWindow {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var prev1, prev2 *TrackMeta

	if len(c.history) >= 1 {
		prev1 = c.history[len(c.history)-1]
	}
	if len(c.history) >= 2 {
		prev2 = c.history[len(c.history)-2]
	}

	return TrackWindow{
		Prev2: WindowSlot{
			Track:    prev2,
			HasArt:   c.hasArtLocked(prev2),
			Position: "prev2",
		},
		Prev1: WindowSlot{
			Track:    prev1,
			HasArt:   c.hasArtLocked(prev1),
			Position: "prev1",
		},
		Current: WindowSlot{
			Track:    c.current,
			HasArt:   c.hasArtLocked(c.current),
			Position: "current",
		},
		Next1: WindowSlot{
			Track:    nil,
			HasArt:   false,
			Position: "next1",
		},
		Next2: WindowSlot{
			Track:    nil,
			HasArt:   false,
			Position: "next2",
		},
	}
}

func (c *CacheState) hasArtLocked(track *TrackMeta) bool {
	if track == nil || track.AlbumImageURL == "" {
		return false
	}
	_, ok := c.artCache[track.AlbumImageURL]
	return ok
}

func warmWindowCache(track *TrackMeta) error {
	cacheState.updateCurrentTrack(track)

	window := cacheState.buildWindow()

	toWarm := []*TrackMeta{
		window.Prev2.Track,
		window.Prev1.Track,
		window.Current.Track,
		window.Next1.Track,
		window.Next2.Track,
	}

	for _, t := range toWarm {
		if t == nil {
			continue
		}
		if err := cacheState.ensureArtProcessed(t); err != nil {
			return err
		}
	}

	return nil
}

func handleWindow(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("live") == "1" {
		track, err := fetchSpotifyCurrentTrack(r)
		if err != nil {
			http.Error(w, "failed to fetch current track", http.StatusBadGateway)
			return
		}

		queue, err := fetchSpotifyQueue(r)
		if err != nil {
			http.Error(w, "failed to fetch queue", http.StatusBadGateway)
			return
		}

		if err := warmWindowCacheWithQueue(track, queue); err != nil {
			http.Error(w, "failed to warm cache", http.StatusBadGateway)
			return
		}

		writeJSON(w, http.StatusOK, cacheState.buildWindowWithQueue(queue))
		return
	}

	track, queue, _ := playbackState.snapshot()
	cacheState.updateCurrentTrack(track)

	window := cacheState.buildWindowWithQueue(queue)
	// Ensure artwork is processed for any slots that claim to have art.
	for _, t := range []*TrackMeta{window.Prev2.Track, window.Prev1.Track, window.Current.Track, window.Next1.Track, window.Next2.Track} {
		_ = cacheState.ensureArtProcessed(t)
	}

	writeJSON(w, http.StatusOK, window)
}

func handleArtworkBySlot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("live") == "1" {
		track, err := fetchSpotifyCurrentTrack(r)
		if err != nil {
			http.Error(w, "failed to fetch current track", http.StatusBadGateway)
			return
		}

		queue, err := fetchSpotifyQueue(r)
		if err != nil {
			http.Error(w, "failed to fetch queue", http.StatusBadGateway)
			return
		}

		if err := warmWindowCacheWithQueue(track, queue); err != nil {
			http.Error(w, "failed to warm cache", http.StatusBadGateway)
			return
		}

		slot := r.URL.Query().Get("slot")
		window := cacheState.buildWindowWithQueue(queue)
		serveArtworkSlot(w, slot, window)
		return
	}

	slot := r.URL.Query().Get("slot")
	track, queue, _ := playbackState.snapshot()
	cacheState.updateCurrentTrack(track)
	window := cacheState.buildWindowWithQueue(queue)
	serveArtworkSlot(w, slot, window)
	return
}

func serveArtworkSlot(w http.ResponseWriter, slot string, window TrackWindow) {

	var selected *TrackMeta
	switch slot {
	case "prev2":
		selected = window.Prev2.Track
	case "prev1":
		selected = window.Prev1.Track
	case "current":
		selected = window.Current.Track
	case "next1":
		selected = window.Next1.Track
	case "next2":
		selected = window.Next2.Track
	default:
		http.Error(w, "invalid slot", http.StatusBadRequest)
		return
	}

	if selected == nil {
		http.Error(w, "slot is empty", http.StatusNotFound)
		return
	}

	if payload, ok := cacheState.getArt(selected); ok {
		writeArtworkPayload(w, selected, payload)
		return
	}

	// Best-effort on-demand processing if the poller hasn't prewarmed yet.
	_ = cacheState.ensureArtProcessed(selected)
	if payload, ok := cacheState.getArt(selected); ok {
		writeArtworkPayload(w, selected, payload)
		return
	}

	http.Error(w, "art not cached", http.StatusNotFound)
	return

}

func writeArtworkPayload(w http.ResponseWriter, selected *TrackMeta, payload []byte) {

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Width", "64")
	w.Header().Set("X-Height", "64")
	w.Header().Set("X-Format", "rgb565-be")
	w.Header().Set("X-Track-Id", selected.ID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func fetchSpotifyQueueCtx(ctx context.Context) ([]*TrackMeta, error) {
	accessToken, err := getValidAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.spotify.com/v1/me/player/queue", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	var raw struct {
		Queue []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			DurationMs int    `json:"duration_ms"`
			Artists    []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Album struct {
				Name   string `json:"name"`
				Images []struct {
					URL string `json:"url"`
				} `json:"images"`
			} `json:"album"`
		} `json:"queue"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	out := make([]*TrackMeta, 0, len(raw.Queue))
	for _, item := range raw.Queue {
		var artistNames []string
		for _, a := range item.Artists {
			if a.Name != "" {
				artistNames = append(artistNames, a.Name)
			}
		}

		imageURL := ""
		if len(item.Album.Images) > 0 {
			imageURL = item.Album.Images[0].URL
		}

		artID := imageURL
		if artID == "" {
			artID = item.ID
		}

		out = append(out, &TrackMeta{
			ID:            item.ID,
			Title:         item.Name,
			Artist:        strings.Join(artistNames, ", "),
			Album:         item.Album.Name,
			AlbumImageURL: imageURL,
			ArtID:         artID,
			IsPlaying:     false,
			ProgressMs:    0,
			DurationMs:    item.DurationMs,
		})
	}

	return out, nil
}

func fetchSpotifyQueue(r *http.Request) ([]*TrackMeta, error) {
	return fetchSpotifyQueueCtx(r.Context())
}

func (c *CacheState) buildWindowWithQueue(queue []*TrackMeta) TrackWindow {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var prev1, prev2 *TrackMeta
	if len(c.history) >= 1 {
		prev1 = c.history[len(c.history)-1]
	}
	if len(c.history) >= 2 {
		prev2 = c.history[len(c.history)-2]
	}

	var next1, next2 *TrackMeta
	if len(queue) >= 1 {
		next1 = queue[0]
	}
	if len(queue) >= 2 {
		next2 = queue[1]
	}

	return TrackWindow{
		Prev2: WindowSlot{
			Track:    prev2,
			HasArt:   c.hasArtLocked(prev2),
			Position: "prev2",
		},
		Prev1: WindowSlot{
			Track:    prev1,
			HasArt:   c.hasArtLocked(prev1),
			Position: "prev1",
		},
		Current: WindowSlot{
			Track:    c.current,
			HasArt:   c.hasArtLocked(c.current),
			Position: "current",
		},
		Next1: WindowSlot{
			Track:    next1,
			HasArt:   c.hasArtLocked(next1),
			Position: "next1",
		},
		Next2: WindowSlot{
			Track:    next2,
			HasArt:   c.hasArtLocked(next2),
			Position: "next2",
		},
	}
}

func warmWindowCacheWithQueue(track *TrackMeta, queue []*TrackMeta) error {
	cacheState.updateCurrentTrack(track)

	for _, t := range queue {
		if t != nil && t.ID != "" {
			cacheState.mu.Lock()
			cacheState.trackCache[t.ID] = t
			cacheState.mu.Unlock()
		}
	}

	window := cacheState.buildWindowWithQueue(queue)

	toWarm := []*TrackMeta{
		window.Prev2.Track,
		window.Prev1.Track,
		window.Current.Track,
		window.Next1.Track,
		window.Next2.Track,
	}

	for _, t := range toWarm {
		if t == nil {
			continue
		}
		if err := cacheState.ensureArtProcessed(t); err != nil {
			return err
		}
	}

	return nil
}

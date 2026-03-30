package main

import (
	"context"
	"log"
	"time"
)

const (
	nowPlayingPollInterval = 2 * time.Second
	preloadLeadMs          = 2000
)

func startPlaybackPoller() {
	go func() {
		// Initial poll so endpoints have data immediately.
		pollOnce(context.Background())

		ticker := time.NewTicker(nowPlayingPollInterval)
		defer ticker.Stop()

		for range ticker.C {
			pollOnce(context.Background())
		}
	}()
}

func pollOnce(ctx context.Context) {
	track, err := fetchSpotifyCurrentTrackCtx(ctx)
	if err != nil {
		playbackState.setPollError(err)
		// Usually auth or network; keep last known state.
		log.Printf("poll current track failed: %v", err)
		return
	}

	prevID := playbackState.lastTrack()
	curID := ""
	if track != nil {
		curID = track.ID
	}

	changed := curID != prevID

	var queue []*TrackMeta
	if track == nil {
		cacheState.updateCurrentTrack(nil)
		playbackState.set(nil, nil)
		if prevID != "" {
			wsHub.broadcastEvent(WsEvent{Type: "stopped", At: time.Now().UnixMilli()})
		}
		return
	}

	// Keep cache state in sync even when unchanged (progress updates).
	cacheState.updateCurrentTrack(track)

	if changed {
		queue, _ = fetchSpotifyQueueCtx(ctx)
		if err := warmWindowCacheWithQueue(track, queue); err != nil {
			_ = warmWindowCache(track)
		}
		playbackState.set(track, queue)
		playbackState.markTrackChanged(curID)

		window := cacheState.buildWindowWithQueue(queue)
		wsHub.broadcastEvent(WsEvent{Type: "track_changed", At: time.Now().UnixMilli(), Track: cloneTrackMeta(track), Window: &window})
		return
	}

	// Not changed: update stored track fields.
	_, oldQueue, _ := playbackState.snapshot()
	playbackState.set(track, oldQueue)

	// Predictive preload: 2s before end, warm next tracks.
	if track.IsPlaying && track.DurationMs > 0 {
		threshold := track.DurationMs - preloadLeadMs
		if threshold < 0 {
			threshold = 0
		}
		if track.ProgressMs >= threshold && playbackState.shouldTriggerPreload(curID) {
			queue, _ = fetchSpotifyQueueCtx(ctx)
			if queue != nil {
				if err := warmWindowCacheWithQueue(track, queue); err == nil {
					playbackState.set(track, queue)
					playbackState.markPreloadDone(curID)

					window := cacheState.buildWindowWithQueue(queue)
					wsHub.broadcastEvent(WsEvent{Type: "preload_ready", At: time.Now().UnixMilli(), Track: cloneTrackMeta(track), Window: &window})
				}
			}
		}
	}
}

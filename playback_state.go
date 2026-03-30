package main

import (
	"sync"
	"time"
)

type PlaybackState struct {
	mu sync.RWMutex

	track     *TrackMeta
	queue     []*TrackMeta
	updatedAt time.Time

	pollErr   string
	pollErrAt time.Time

	lastTrackID       string
	preloadDoneForID  string
	lastPreloadSentID string
}

var playbackState = &PlaybackState{}

func cloneTrackMeta(t *TrackMeta) *TrackMeta {
	if t == nil {
		return nil
	}
	out := *t
	return &out
}

func cloneQueue(in []*TrackMeta) []*TrackMeta {
	if len(in) == 0 {
		return nil
	}
	out := make([]*TrackMeta, 0, len(in))
	for _, t := range in {
		out = append(out, cloneTrackMeta(t))
	}
	return out
}

func (p *PlaybackState) set(track *TrackMeta, queue []*TrackMeta) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.track = cloneTrackMeta(track)
	p.queue = cloneQueue(queue)
	p.updatedAt = time.Now()

	// Clear error on successful poll.
	p.pollErr = ""
	p.pollErrAt = time.Time{}

	if track == nil {
		p.lastTrackID = ""
		p.preloadDoneForID = ""
		p.lastPreloadSentID = ""
		return
	}

	// Only update lastTrackID here when poller says it changed.
}

func (p *PlaybackState) setPollError(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pollErr = err.Error()
	p.pollErrAt = time.Now()
}

func (p *PlaybackState) pollError() (string, time.Time) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pollErr, p.pollErrAt
}

func (p *PlaybackState) getTrack() *TrackMeta {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneTrackMeta(p.track)
}

func (p *PlaybackState) snapshot() (*TrackMeta, []*TrackMeta, time.Time) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneTrackMeta(p.track), cloneQueue(p.queue), p.updatedAt
}

func (p *PlaybackState) markTrackChanged(newID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastTrackID = newID
	p.preloadDoneForID = ""
	p.lastPreloadSentID = ""
}

func (p *PlaybackState) lastTrack() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastTrackID
}

func (p *PlaybackState) shouldTriggerPreload(trackID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return trackID != "" && p.preloadDoneForID != trackID
}

func (p *PlaybackState) markPreloadDone(trackID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preloadDoneForID = trackID
}

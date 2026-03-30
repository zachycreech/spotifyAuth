package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	host       = "0.0.0.0"
	port       = "8787"
	tokenFile  = "spotify_tokens.json"
	stateFile  = "spotify_state.txt"
	authURL    = "https://accounts.spotify.com/authorize"
	tokenURL   = "https://accounts.spotify.com/api/token"
	nowPlayURL = "https://api.spotify.com/v1/me/player/currently-playing"
)

var (
	clientID     = os.Getenv("SPOTIFY_CLIENT_ID")
	clientSecret = os.Getenv("SPOTIFY_CLIENT_SECRET")
	redirectURI  = getenv("SPOTIFY_REDIRECT_URI", "http://127.0.0.1:8787/callback")
	scopes       = "user-read-currently-playing user-read-playback-state"
	httpClient   = &http.Client{Timeout: 15 * time.Second}
	tokenMu      sync.Mutex
)

type TokenData struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	SavedAt      int64  `json:"saved_at"`
}

type NowPlayingResponse struct {
	OK            bool    `json:"ok"`
	IsPlaying     bool    `json:"is_playing"`
	ID            *string `json:"id,omitempty"`
	Title         *string `json:"title"`
	Artist        *string `json:"artist"`
	Album         *string `json:"album"`
	ArtID         *string `json:"art_id,omitempty"`
	ProgressMs    int     `json:"progress_ms"`
	DurationMs    int     `json:"duration_ms"`
	AlbumImageURL *string `json:"album_image_url"`
}

func main() {
	if clientID == "" || clientSecret == "" {
		log.Fatal("SPOTIFY_CLIENT_ID and SPOTIFY_CLIENT_SECRET must be set")
	}

	startPlaybackPoller()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/login", handleLogin)
	mux.HandleFunc("/callback", handleCallback)
	mux.HandleFunc("/nowplaying", handleNowPlaying)
	mux.HandleFunc("/ws", handleWS)
	mux.HandleFunc("/artwork", handleArtwork)
	mux.HandleFunc("/preview", handlePreview)
	mux.HandleFunc("/artwork-preview", handleArtworkPreview)
	mux.HandleFunc("/window", handleWindow)
	mux.HandleFunc("/artwork-slot", handleArtworkBySlot)

	addr := host + ":" + port
	log.Printf("Listening on http://%s", addr)
	log.Printf("Login at %s/login", "http://127.0.0.1:"+port)
	log.Fatal(http.ListenAndServe(addr, mux))
	log.Println("Redirect URI:", redirectURI)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"message":        "Spotify helper is running",
		"login_url":      "/login",
		"nowplaying_url": "/nowplaying",
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randomURLSafe(24)
	if err != nil {
		http.Error(w, "failed to generate state", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(stateFile, []byte(state), 0600); err != nil {
		http.Error(w, "failed to persist state", http.StatusInternalServerError)
		return
	}

	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("response_type", "code")
	v.Set("redirect_uri", redirectURI)
	v.Set("scope", scopes)
	v.Set("state", state)
	v.Set("show_dialog", "true")

	http.Redirect(w, r, authURL+"?"+v.Encode(), http.StatusFound)
}

func handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	expectedState, err := os.ReadFile(stateFile)
	if err != nil {
		http.Error(w, "missing saved state", http.StatusBadRequest)
		return
	}
	if code == "" || state == "" || state != strings.TrimSpace(string(expectedState)) {
		http.Error(w, "invalid code/state", http.StatusBadRequest)
		return
	}

	td, err := exchangeCodeForToken(r.Context(), code)
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := saveTokens(td); err != nil {
		http.Error(w, "failed to save tokens", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "Spotify authorization complete. You can close this tab.",
	})
}

func handleNowPlaying(w http.ResponseWriter, r *http.Request) {
	// Default behavior serves the last polled state (2s poll loop) so clients
	// don't directly hit Spotify on every request.
	if r.URL.Query().Get("live") == "1" {
		track, err := fetchSpotifyCurrentTrackCtx(r.Context())
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"ok":    false,
				"error": "not_authorized",
				"login": "/login",
			})
			return
		}
		writeJSON(w, http.StatusOK, nowPlayingResponseFromTrack(track))
		return
	}

	track, _, updatedAt := playbackState.snapshot()
	if track == nil && updatedAt.IsZero() {
		if errStr, _ := playbackState.pollError(); errStr != "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"ok":    false,
				"error": "not_authorized",
				"login": "/login",
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, nowPlayingResponseFromTrack(track))
}

func nowPlayingResponseFromTrack(track *TrackMeta) NowPlayingResponse {
	if track == nil {
		return NowPlayingResponse{OK: true, IsPlaying: false, ProgressMs: 0, DurationMs: 0}
	}

	id := track.ID
	artID := track.ArtID
	title := track.Title
	artist := track.Artist
	album := track.Album
	imageURL := track.AlbumImageURL

	return NowPlayingResponse{
		OK:            true,
		IsPlaying:     track.IsPlaying,
		ID:            strPtrOrNil(id),
		Title:         strPtrOrNil(title),
		Artist:        strPtrOrNil(artist),
		Album:         strPtrOrNil(album),
		ArtID:         strPtrOrNil(artID),
		ProgressMs:    track.ProgressMs,
		DurationMs:    track.DurationMs,
		AlbumImageURL: strPtrOrNil(imageURL),
	}
}

func exchangeCodeForToken(ctx context.Context, code string) (*TokenData, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+basicAuth())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify token endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	var td TokenData
	if err := json.Unmarshal(body, &td); err != nil {
		return nil, err
	}
	td.SavedAt = time.Now().Unix()
	return &td, nil
}

func refreshAccessToken(ctx context.Context) (*TokenData, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()

	td, err := loadTokens()
	if err != nil {
		return nil, err
	}
	if td.RefreshToken == "" {
		return nil, errors.New("missing refresh_token")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", td.RefreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+basicAuth())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify refresh returned %d: %s", resp.StatusCode, string(body))
	}

	var refreshed TokenData
	if err := json.Unmarshal(body, &refreshed); err != nil {
		return nil, err
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = td.RefreshToken
	}
	refreshed.SavedAt = time.Now().Unix()

	if err := saveTokens(&refreshed); err != nil {
		return nil, err
	}
	return &refreshed, nil
}

func getValidAccessToken(ctx context.Context) (string, error) {
	td, err := loadTokens()
	if err != nil {
		return "", err
	}

	expiresAt := td.SavedAt + int64(td.ExpiresIn) - 60
	if time.Now().Unix() >= expiresAt {
		td, err = refreshAccessToken(ctx)
		if err != nil {
			return "", err
		}
	}

	if td.AccessToken == "" {
		return "", errors.New("missing access token")
	}
	return td.AccessToken, nil
}

func saveTokens(td *TokenData) error {
	b, err := json.MarshalIndent(td, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(tokenFile, b, 0600)
}

func loadTokens() (*TokenData, error) {
	b, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}
	var td TokenData
	if err := json.Unmarshal(b, &td); err != nil {
		return nil, err
	}
	return &td, nil
}

func basicAuth() string {
	raw := clientID + ":" + clientSecret
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func getenv(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

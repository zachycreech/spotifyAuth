# Spotify Auth Service

Simple Go service that handles Spotify authentication and exposes endpoints for a local consumer (such as an ESP32 display).

This service manages Spotify OAuth and provides a simplified API for retrieving currently playing track data and album artwork.

---

## 🚀 Setup

### 1. Clone the repo

```bash
git clone git@github.com:zachycreech/spotifyAuth.git
cd spotifyAuth
```

---

### 2. Create environment file

Create a `.env` file in the project root:

```
SPOTIFY_CLIENT_ID=your_client_id
SPOTIFY_CLIENT_SECRET=your_client_secret
SPOTIFY_REDIRECT_URI=http://127.0.0.1:8787/callback
```

---

### 3. Run the service

```bash
go run .
```

Or build:

```bash
go build -o spotifyAuth
./spotifyAuth
```

---

## 🔐 Authentication Flow

This service uses Spotify’s OAuth2 Authorization Code flow.

After initial authorization, the service handles token refresh automatically.

---

## 🌐 Endpoints

### `/login`

Starts the Spotify login flow.

---

### `/callback`

OAuth redirect endpoint used by Spotify after login.

---

### `/nowplaying`

Returns the currently playing track:

```json
{
  "ok": true,
  "is_playing": true,
  "title": "Song Title",
  "artist": "Artist Name",
  "album": "Album Name",
  "progress_ms": 12345,
  "duration_ms": 200000,
  "album_image_url": "https://..."
}
```

---

### `/window`

Returns a playback window of tracks:

```json
{
  "prev2": {...},
  "prev1": {...},
  "current": {...},
  "next1": {...},
  "next2": {...}
}
```

---

### `/artwork-slot?slot=<slot>`

Returns album artwork for a given slot in RGB565 format.

Valid slots:
- `prev2`
- `prev1`
- `current`
- `next1`
- `next2`

Format:
- 64x64 image
- RGB565
- 8192 bytes

---

### `/preview`

Renders a terminal preview of processed artwork for debugging.

---

## 🧠 How it works

1. User authenticates via Spotify OAuth
2. Service stores and refreshes tokens
3. Queries Spotify Web API for playback state
4. Processes album artwork into RGB565 format
5. Exposes lightweight endpoints for external clients (e.g., ESP32)

---

## ⚠️ Notes

- `redirect_uri` must exactly match what is configured in the Spotify Developer Dashboard
- Authentication requires manual approval on first login
- This service is intended for local/personal use

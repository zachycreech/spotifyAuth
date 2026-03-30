package main

import (
	"encoding/json"
	"hash/crc32"
	"image"
	"io"
	"net/http"
	"strconv"
)

const (
	artWidth  = 64
	artHeight = 64
)

func rgb888To565(r, g, b uint8) uint16 {
	return (uint16(r&0xF8) << 8) | (uint16(g&0xFC) << 3) | (uint16(b) >> 3)
}

func imageToRGB565(img image.Image) []byte {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	out := make([]byte, 0, width*height*2)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			r8 := uint8(r >> 8)
			g8 := uint8(g >> 8)
			b8 := uint8(b >> 8)

			rgb565 := rgb888To565(r8, g8, b8)

			// big-endian: high byte first
			out = append(out, byte(rgb565>>8), byte(rgb565&0xFF))
		}
	}

	return out
}

func processAlbumArt(imageURL string) ([]byte, error) {
	img, err := fetchImage(imageURL)
	if err != nil {
		return nil, err
	}

	img = cropToSquare(img)
	img = resizeTo64(img)

	return imageToRGB565(img), nil
}

func handleArtwork(w http.ResponseWriter, r *http.Request) {
	accessToken, err := getValidAccessToken(r.Context())
	if err != nil {
		http.Error(w, "not authorized", http.StatusUnauthorized)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, nowPlayURL, nil)
	if err != nil {
		http.Error(w, "failed to build request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, "spotify request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		http.Error(w, "nothing currently playing", http.StatusNoContent)
		return
	}

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "spotify returned non-200", http.StatusBadGateway)
		return
	}

	var data struct {
		Item *struct {
			ID    string `json:"id"`
			Album struct {
				Images []struct {
					URL string `json:"url"`
				} `json:"images"`
			} `json:"album"`
		} `json:"item"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		http.Error(w, "failed to decode spotify response", http.StatusBadGateway)
		return
	}

	if data.Item == nil || len(data.Item.Album.Images) == 0 {
		http.Error(w, "no artwork found", http.StatusBadRequest)
		return
	}

	imageURL := data.Item.Album.Images[0].URL

	payload, err := processAlbumArt(imageURL)
	if err != nil {
		http.Error(w, "failed to process artwork", http.StatusBadGateway)
		return
	}

	checksum := crc32.ChecksumIEEE(payload)

	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.Itoa(len(payload)))
	h.Set("X-CRC32", strconv.FormatUint(uint64(checksum), 10))
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "close")

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func handleArtworkPreview(w http.ResponseWriter, r *http.Request) {
	resp, err := http.Get("http://127.0.0.1:8787/artwork")
	if err != nil {
		http.Error(w, "failed to fetch artwork", 500)
		return
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "failed to read artwork", 500)
		return
	}

	img := rgb565ToImage(data, 64, 64)

	savePreview(img)
	printImageToTerminal(img)

	w.Write([]byte("rgb565 preview generated\n"))
}

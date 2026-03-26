package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"encoding/json"

	"golang.org/x/image/draw"
)

func fetchImage(url string) (image.Image, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	img, _, err := image.Decode(resp.Body)
	return img, err
}

func cropToSquare(img image.Image) image.Image {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()

	size := w
	if h < w {
		size = h
	}

	offsetX := (w - size) / 2
	offsetY := (h - size) / 2

	rect := image.Rect(0, 0, size, size)
	dst := image.NewRGBA(rect)

	draw.Draw(dst, rect, img, image.Point{
		X: b.Min.X + offsetX,
		Y: b.Min.Y + offsetY,
	}, draw.Src)

	return dst
}

func resizeTo64(img image.Image) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, 64, 64))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	return dst
}

func savePreview(img image.Image) {
	f, err := os.Create("preview.png")
	if err != nil {
		fmt.Println("failed to save preview:", err)
		return
	}
	defer f.Close()

	png.Encode(f, img)
}

func rgb8(c color.Color) (uint8, uint8, uint8) {
	r, g, b, _ := c.RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

func printImageToTerminal(img image.Image) {
	b := img.Bounds()

	for y := 0; y < b.Dy(); y += 2 {
		for x := 0; x < b.Dx(); x++ {
			tr, tg, tb := rgb8(img.At(x, y))

			var br, bg, bb uint8
			if y+1 < b.Dy() {
				br, bg, bb = rgb8(img.At(x, y+1))
			}

			fmt.Printf(
				"\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀",
				tr, tg, tb,
				br, bg, bb,
			)
		}
		fmt.Print("\x1b[0m\n")
	}
}

func handlePreview(w http.ResponseWriter, r *http.Request) {
	accessToken, err := getValidAccessToken(r.Context())
	if err != nil {
		http.Error(w, "not authorized", http.StatusUnauthorized)
		return
	}

	req, _ := http.NewRequest("GET", nowPlayURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, "spotify request failed", 500)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		http.Error(w, "no track playing", 400)
		return
	}

	var data struct {
		Item struct {
			Album struct {
				Images []struct {
					URL string `json:"url"`
				} `json:"images"`
			} `json:"album"`
		} `json:"item"`
	}

	json.NewDecoder(resp.Body).Decode(&data)

	if len(data.Item.Album.Images) == 0 {
		http.Error(w, "no image", 400)
		return
	}

	url := data.Item.Album.Images[0].URL

	img, err := fetchImage(url)
	if err != nil {
		http.Error(w, "failed to fetch image", 500)
		return
	}

	img = cropToSquare(img)
	img = resizeTo64(img)

	savePreview(img)
	printImageToTerminal(img)

	w.Write([]byte("preview generated\n"))
}



func rgb565ToImage(data []byte, width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	i := 0
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if i+1 >= len(data) {
				break
			}

			// big-endian
			val := uint16(data[i])<<8 | uint16(data[i+1])
			i += 2

			r := uint8((val>>11)&0x1F) << 3
			g := uint8((val>>5)&0x3F) << 2
			b := uint8(val&0x1F) << 3

			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}

	return img
}

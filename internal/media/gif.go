package media

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"io"

	"golang.org/x/image/draw"
)

// maxGIFBytes limita o GIF lido de uma vez para contar quadros (64 MiB;
// GIF animado costuma ser pequeno, e acima disso o ffmpeg cobre via vídeo).
const maxGIFBytes = 64 << 20

// IsAnimatedGIF diz se os bytes são um GIF com mais de um quadro. GIF de um
// quadro só segue o caminho normal de imagem.
func IsAnimatedGIF(data io.Reader) (bool, error) {
	limited := io.LimitReader(data, maxGIFBytes+1)
	decoded, err := gif.DecodeAll(limited)
	if err != nil {
		return false, fmt.Errorf("não foi possível ler o GIF: %w", err)
	}
	return len(decoded.Image) > 1, nil
}

// ExtractGIFFrames decodifica até maxFrames quadros espaçados do GIF animado
// (bytes já lidos), escalados para caber em maxSide no lado maior e
// codificados em JPEG. Devolve os quadros e a duração total em segundos.
func ExtractGIFFrames(data []byte, maxFrames, maxSide int) ([]Frame, float64, error) {
	if len(data) > maxGIFBytes {
		return nil, 0, fmt.Errorf("GIF grande demais (%s, limite %s) para extrair quadros", formatBytes(int64(len(data))), formatBytes(maxGIFBytes))
	}
	if maxFrames <= 0 {
		maxFrames = DefaultMaxFrames
	}
	if maxSide <= 0 {
		maxSide = DefaultMaxSide
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("não foi possível ler o GIF: %w", err)
	}
	total := len(decoded.Image)
	if total == 0 {
		return nil, 0, fmt.Errorf("GIF sem quadros")
	}
	duration := gifDurationSec(decoded)
	pick := spacedIndexes(total, maxFrames)
	frames := make([]Frame, 0, len(pick))
	for position, index := range pick {
		scaled := scaleImageToSide(decoded.Image[index], maxSide)
		var encoded bytes.Buffer
		if err := jpeg.Encode(&encoded, scaled, &jpeg.Options{Quality: 85}); err != nil {
			return nil, 0, fmt.Errorf("codificar quadro %d do GIF: %w", index+1, err)
		}
		timestamp := 0.0
		if duration > 0 && len(pick) > 0 {
			timestamp = duration * float64(position) / float64(len(pick))
		}
		frames = append(frames, Frame{Data: bytes.Clone(encoded.Bytes()), TimestampSec: timestamp})
	}
	return frames, duration, nil
}

func gifDurationSec(decoded *gif.GIF) float64 {
	total := 0.0
	for _, delay := range decoded.Delay {
		total += float64(delay) / 100.0
	}
	return total
}

// spacedIndexes escolhe até max quadros espaçados de total, sempre incluindo
// o primeiro e, quando há mais de um, o último.
func spacedIndexes(total, max int) []int {
	if total <= 0 || max <= 0 {
		return nil
	}
	if total <= max {
		indexes := make([]int, total)
		for i := range indexes {
			indexes[i] = i
		}
		return indexes
	}
	indexes := make([]int, 0, max)
	for i := 0; i < max; i++ {
		indexes = append(indexes, i*(total-1)/(max-1))
	}
	return indexes
}

// scaleImageToSide reduz a imagem para caber em maxSide no lado maior,
// mantendo a proporção. Não amplia imagens menores.
func scaleImageToSide(src image.Image, maxSide int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	largest := max(width, height)
	if largest <= maxSide || largest <= 0 {
		return src
	}
	scale := float64(maxSide) / float64(largest)
	dst := image.NewRGBA(image.Rect(0, 0, int(float64(width)*scale+0.5), int(float64(height)*scale+0.5)))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)
	return dst
}

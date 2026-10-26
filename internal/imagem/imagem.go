// Package imagem reduz fotos grandes do ERP para caberem no WhatsApp (limite de 5 MB
// por imagem) e carregarem rápido no celular.
package imagem

import (
	"bytes"
	"image"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
)

const (
	LimiteBytes = 1 << 20 // acima disso, reduz
	MaxLado     = 1600    // px do maior lado depois de reduzir
)

// Reduzir devolve a foto como veio se ela já for pequena. Se for maior que
// LimiteBytes, redimensiona para no máximo MaxLado px e regrava em JPEG. Se não
// conseguir decodificar (formato desconhecido), devolve o original.
func Reduzir(foto []byte) []byte {
	if len(foto) <= LimiteBytes {
		return foto
	}
	orig, _, err := image.Decode(bytes.NewReader(foto))
	if err != nil {
		return foto
	}
	b := orig.Bounds()
	w, h := b.Dx(), b.Dy()
	if maior := max(w, h); maior > MaxLado {
		w, h = w*MaxLado/maior, h*MaxLado/maior
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), orig, b, draw.Src, nil)

	for _, qualidade := range []int{85, 70, 55} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: qualidade}); err != nil {
			return foto
		}
		if buf.Len() <= LimiteBytes || qualidade == 55 {
			return buf.Bytes()
		}
	}
	return foto
}

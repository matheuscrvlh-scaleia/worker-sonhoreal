package imagem

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"testing"
)

func TestPequenaVoltaIgual(t *testing.T) {
	foto := []byte("qualquer coisa pequena")
	if got := Reduzir(foto); !bytes.Equal(got, foto) {
		t.Error("foto pequena não deveria mudar")
	}
}

func TestGrandeReduz(t *testing.T) {
	// PNG de ruído 3000x2000: não comprime, fica bem acima de 1 MB.
	rnd := rand.New(rand.NewPCG(1, 2))
	img := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	for y := 0; y < 2000; y++ {
		for x := 0; x < 3000; x++ {
			img.Set(x, y, color.RGBA{uint8(rnd.IntN(256)), uint8(rnd.IntN(256)), uint8(rnd.IntN(256)), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if buf.Len() <= LimiteBytes {
		t.Fatalf("imagem de teste pequena demais: %d", buf.Len())
	}

	out := Reduzir(buf.Bytes())
	if len(out) > LimiteBytes {
		t.Errorf("ainda grande: %d bytes", len(out))
	}
	cfg, formato, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || formato != "jpeg" || cfg.Width != MaxLado || cfg.Height != 1066 {
		t.Errorf("saída: %s %dx%d err=%v", formato, cfg.Width, cfg.Height, err)
	}
}

func TestFormatoDesconhecidoVoltaIgual(t *testing.T) {
	foto := bytes.Repeat([]byte{0x42}, LimiteBytes+10)
	if got := Reduzir(foto); !bytes.Equal(got, foto) {
		t.Error("formato desconhecido deveria voltar o original")
	}
}

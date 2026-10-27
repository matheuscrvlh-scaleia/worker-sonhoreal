package replica

import (
	"reflect"
	"strings"
	"testing"

	"ponte/internal/consultas"
)

func extracao(perc int) *consultas.Extracao {
	return &consultas.Extracao{Tabela: "estoque", Chave: []string{"codigo", "filial"}, MaxRemocaoPerc: perc}
}

func linhas(n int) []map[string]any {
	var out []map[string]any
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"codigo": int64(i), "filial": "01", "estoque": 2.0})
	}
	return out
}

func TestPlanejarSoADiferenca(t *testing.T) {
	e := extracao(30)
	primeiro, err := planejar(e, linhas(3), map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(primeiro.mudou, []string{"0|01", "1|01", "2|01"}) || len(primeiro.remover) != 0 {
		t.Fatalf("primeira carga: mudou=%v remover=%v", primeiro.mudou, primeiro.remover)
	}
	l := primeiro.atual["1|01"].dados
	if l["chave"] != "1|01" || len(l["hash"].(string)) != 16 {
		t.Errorf("linha sem chave/hash: %v", l)
	}

	base := map[string]string{}
	for k, l := range primeiro.atual {
		base[k] = l.hash
	}
	// Mesmo conteúdo: nada a enviar. Uma linha mudou e uma sumiu: só elas.
	if p, _ := planejar(e, linhas(3), base); len(p.mudou)+len(p.remover) != 0 {
		t.Errorf("sem mudança, mas mudou=%v remover=%v", p.mudou, p.remover)
	}
	novas := linhas(2)
	novas[0]["estoque"] = 7.0
	p, err := planejar(e, novas, base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.mudou, []string{"0|01"}) || !reflect.DeepEqual(p.remover, []string{"2|01"}) {
		t.Errorf("mudou=%v remover=%v", p.mudou, p.remover)
	}
}

func TestPlanejarRecusaApagarDemais(t *testing.T) {
	base := map[string]string{}
	p, _ := planejar(extracao(30), linhas(200), base)
	for k, l := range p.atual {
		base[k] = l.hash
	}
	if _, err := planejar(extracao(30), nil, base); err == nil || !strings.Contains(err.Error(), "0 linhas") {
		t.Errorf("ERP vazio deveria travar: %v", err)
	}
	if _, err := planejar(extracao(30), linhas(100), base); err == nil {
		t.Error("apagar 100 de 200 (50%) deveria travar com limite de 30%")
	}
	if _, err := planejar(extracao(30), linhas(160), base); err != nil {
		t.Errorf("apagar 40 de 200 está dentro do mínimo de 50: %v", err)
	}
}

func TestPlanejarChave(t *testing.T) {
	e := extracao(30)
	if _, err := planejar(e, []map[string]any{{"codigo": 1}}, nil); err == nil {
		t.Error("coluna da chave ausente deveria dar erro")
	}
	dup := append(linhas(2), map[string]any{"codigo": int64(1), "filial": "01", "estoque": 9.0})
	p, err := planejar(e, dup, nil)
	if err != nil || p.repetidas != 1 || len(p.atual) != 2 {
		t.Errorf("repetidas=%d atual=%d err=%v", p.repetidas, len(p.atual), err)
	}
}

package replica

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cliente fala com a Edge Function "fdc" (rotas /sync/*), com o token do worker.
type Cliente struct {
	base  string
	token string
	http  *http.Client
}

func NovoCliente(base, token string) *Cliente {
	return &Cliente{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 2 * time.Minute}}
}

type Comando struct {
	ID       any            `json:"id"` // bigint: chega como texto
	Tipo     string         `json:"tipo"`
	Consulta string         `json:"consulta"`
	Params   map[string]any `json:"params"`
}

func (c *Cliente) Ping(ctx context.Context) error {
	return c.json(ctx, http.MethodGet, "/sync/ping", nil, nil)
}

// Aplicar grava as linhas (cada uma já com "chave" e "hash") e apaga as chaves em remover.
func (c *Cliente) Aplicar(ctx context.Context, tabela string, linhas []map[string]any, remover []string) error {
	return c.json(ctx, http.MethodPost, "/sync/"+url.PathEscape(tabela),
		map[string]any{"linhas": linhas, "remover": remover}, nil)
}

func (c *Cliente) Concluir(ctx context.Context, tabela string, linhas int) error {
	return c.json(ctx, http.MethodPost, "/sync/"+url.PathEscape(tabela)+"/concluir", map[string]any{"linhas": linhas}, nil)
}

// Hashes devolve chave -> hash de tudo que está gravado no Supabase para a tabela.
func (c *Cliente) Hashes(ctx context.Context, tabela string) (map[string]string, error) {
	out := map[string]string{}
	depois := ""
	for {
		var pag struct {
			Hashes [][2]string `json:"hashes"`
			Fim    bool        `json:"fim"`
		}
		caminho := "/sync/" + url.PathEscape(tabela) + "/hashes?depois=" + url.QueryEscape(depois)
		if err := c.json(ctx, http.MethodGet, caminho, nil, &pag); err != nil {
			return nil, err
		}
		for _, h := range pag.Hashes {
			out[h[0]] = h[1]
		}
		if pag.Fim || len(pag.Hashes) == 0 {
			return out, nil
		}
		depois = pag.Hashes[len(pag.Hashes)-1][0]
	}
}

func (c *Cliente) EnviarFoto(ctx context.Context, codigo, seq, hash string, img []byte) error {
	caminho := fmt.Sprintf("/sync/foto/%s/%s?hash=%s", url.PathEscape(codigo), url.PathEscape(seq), url.QueryEscape(hash))
	return c.chamar(ctx, http.MethodPut, caminho, http.DetectContentType(img), bytes.NewReader(img), nil)
}

func (c *Cliente) RemoverFotos(ctx context.Context, chaves []string) error {
	return c.json(ctx, http.MethodPost, "/sync/foto/remover", map[string]any{"chaves": chaves}, nil)
}

func (c *Cliente) Status(ctx context.Context, s map[string]any) error {
	return c.json(ctx, http.MethodPost, "/sync/status", s, nil)
}

func (c *Cliente) Comandos(ctx context.Context) ([]Comando, error) {
	var r struct {
		Comandos []Comando `json:"comandos"`
	}
	err := c.json(ctx, http.MethodGet, "/sync/comandos", nil, &r)
	return r.Comandos, err
}

func (c *Cliente) Responder(ctx context.Context, id any, resultado any, erro string) error {
	corpo := map[string]any{"resultado": resultado}
	if erro != "" {
		corpo["erro"] = erro
	}
	return c.json(ctx, http.MethodPost, "/sync/comandos/"+url.PathEscape(fmt.Sprint(id)), corpo, nil)
}

func (c *Cliente) json(ctx context.Context, metodo, caminho string, entrada, saida any) error {
	var corpo io.Reader
	if entrada != nil {
		b, err := json.Marshal(entrada)
		if err != nil {
			return err
		}
		corpo = bytes.NewReader(b)
	}
	return c.chamar(ctx, metodo, caminho, "application/json", corpo, saida)
}

func (c *Cliente) chamar(ctx context.Context, metodo, caminho, tipo string, corpo io.Reader, saida any) error {
	req, err := http.NewRequestWithContext(ctx, metodo, c.base+caminho, corpo)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if corpo != nil {
		req.Header.Set("Content-Type", tipo)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Erro string `json:"erro"`
		}
		if json.Unmarshal(b, &e) != nil || e.Erro == "" {
			e.Erro = strings.TrimSpace(string(b))
		}
		return fmt.Errorf("%s %s: HTTP %d: %s", metodo, strings.SplitN(caminho, "?", 2)[0], resp.StatusCode, e.Erro)
	}
	if saida != nil {
		return json.Unmarshal(b, saida)
	}
	return nil
}

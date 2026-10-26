// Package api expõe as consultas do queries.yaml via HTTP.
//
//	GET  /health              sem token; 200 se o banco responde, 503 se não
//	GET  /foto/{codigo}/{seq}.jpg  sem token; foto do produto (consulta foto_produto)
//	GET  /q                   lista as consultas (para montar as tools da IA)
//	GET  /q/{nome}?a=1&b=2    executa (também aceita POST com JSON)
//	POST /admin/sql           SQL livre, só com token_admin e sql_livre=true
//	POST /admin/atualizar     pede ao agente para buscar atualização agora
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ponte/internal/banco"
	"ponte/internal/config"
	"ponte/internal/consultas"
	"ponte/internal/imagem"
	"ponte/internal/versao"
)

type Servidor struct {
	cfg    *config.Config
	banco  *banco.Banco
	log    *log.Logger
	inicio time.Time

	conjunto atomic.Pointer[consultas.Conjunto]
	mu       sync.Mutex
	erroQ    string    // último erro ao recarregar o queries.yaml
	modQ     time.Time // mtime do queries.yaml carregado
}

func Novo(cfg *config.Config, b *banco.Banco, l *log.Logger) *Servidor {
	return &Servidor{cfg: cfg, banco: b, log: l, inicio: time.Now()}
}

func (s *Servidor) caminhoQueries() string { return s.cfg.Caminho("queries.yaml") }

// CarregarConsultas lê o queries.yaml. Se o arquivo novo tiver erro, as consultas
// anteriores continuam valendo e o erro aparece no /health.
func (s *Servidor) CarregarConsultas() error {
	st, err := os.Stat(s.caminhoQueries())
	if err == nil {
		var b []byte
		if b, err = os.ReadFile(s.caminhoQueries()); err == nil {
			var cj *consultas.Conjunto
			if cj, err = consultas.Carregar(b); err == nil {
				s.conjunto.Store(cj)
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st != nil {
		s.modQ = st.ModTime()
	}
	if err != nil {
		s.erroQ = err.Error()
		s.log.Printf("queries.yaml NÃO carregado (mantendo o anterior): %v", err)
		return err
	}
	s.erroQ = ""
	s.log.Printf("queries.yaml carregado: %d consultas (sha %s)", len(s.conjunto.Load().Consultas), s.conjunto.Load().SHA256[:12])
	return nil
}

// VigiarConsultas recarrega o queries.yaml sempre que o arquivo muda.
func (s *Servidor) VigiarConsultas(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st, err := os.Stat(s.caminhoQueries())
			if err != nil {
				continue
			}
			s.mu.Lock()
			mudou := !st.ModTime().Equal(s.modQ)
			s.mu.Unlock()
			if mudou {
				s.CarregarConsultas()
			}
		}
	}
}

func (s *Servidor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /foto/{codigo}/{arquivo}", s.foto)
	mux.HandleFunc("GET /q", s.autenticado(false, s.listar))
	mux.HandleFunc("GET /q/{nome}", s.autenticado(false, s.executar))
	mux.HandleFunc("POST /q/{nome}", s.autenticado(false, s.executar))
	mux.HandleFunc("POST /admin/sql", s.autenticado(true, s.sqlLivre))
	mux.HandleFunc("POST /admin/atualizar", s.autenticado(true, s.atualizarAgora))
	return s.registrar(mux)
}

// ---------- rotas ----------

func (s *Servidor) health(w http.ResponseWriter, r *http.Request) {
	ini := time.Now()
	errBanco := s.banco.Ping(r.Context())
	s.mu.Lock()
	erroQ := s.erroQ
	s.mu.Unlock()

	resp := map[string]any{
		"ok":          errBanco == nil,
		"versao":      versao.Versao,
		"cliente":     s.cfg.Cliente,
		"banco_ok":    errBanco == nil,
		"banco_ms":    time.Since(ini).Milliseconds(),
		"no_ar_desde": s.inicio.Format(time.RFC3339),
	}
	if cj := s.conjunto.Load(); cj != nil {
		resp["consultas"] = len(cj.Consultas)
		resp["consultas_sha"] = cj.SHA256
	}
	if erroQ != "" {
		resp["consultas_erro"] = erroQ
	}
	if errBanco != nil && s.tokenOK(r, s.cfg.API.Token) {
		resp["banco_erro"] = errBanco.Error() // detalhe só para quem tem token
	}
	status := http.StatusOK
	if errBanco != nil {
		status = http.StatusServiceUnavailable
	}
	responder(w, status, resp)
}

func (s *Servidor) listar(w http.ResponseWriter, r *http.Request) {
	cj := s.conjunto.Load()
	if cj == nil {
		responder(w, http.StatusServiceUnavailable, erro("queries.yaml não carregado"))
		return
	}
	var out []map[string]any
	for _, c := range cj.Publicas() {
		var ps []*consultas.Param
		for _, p := range c.Params {
			if !p.Fixo {
				ps = append(ps, p)
			}
		}
		out = append(out, map[string]any{"nome": c.Nome, "descricao": strings.TrimSpace(c.Descricao), "params": ps})
	}
	responder(w, http.StatusOK, map[string]any{"consultas": out})
}

func (s *Servidor) executar(w http.ResponseWriter, r *http.Request) {
	cj := s.conjunto.Load()
	if cj == nil {
		responder(w, http.StatusServiceUnavailable, erro("queries.yaml não carregado"))
		return
	}
	nome := r.PathValue("nome")
	c, ok := cj.Consultas[nome]
	if !ok {
		responder(w, http.StatusNotFound, erro("consulta não existe: "+nome))
		return
	}
	if c.Admin && !s.tokenOK(r, s.cfg.API.TokenAdmin) {
		responder(w, http.StatusForbidden, erro("consulta exige token_admin"))
		return
	}
	if c.Resposta == "imagem" {
		responder(w, http.StatusBadRequest, erro("consulta de imagem: use /foto/{codigo}/{seq}.jpg"))
		return
	}
	params, err := lerParams(r)
	if err != nil {
		responder(w, http.StatusBadRequest, erro(err.Error()))
		return
	}
	query, args, err := c.Montar(params)
	if err != nil {
		responder(w, http.StatusBadRequest, erro(err.Error()))
		return
	}
	s.rodar(w, r, nome, query, args, limite(c.MaxLinhas, s.cfg.API.MaxLinhas))
}

func (s *Servidor) sqlLivre(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.API.SQLLivre {
		responder(w, http.StatusForbidden, erro("sql_livre desligado no config.json"))
		return
	}
	var corpo struct {
		SQL    string `json:"sql"`
		Params []any  `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&corpo); err != nil {
		responder(w, http.StatusBadRequest, erro("JSON inválido: "+err.Error()))
		return
	}
	inicio := strings.ToUpper(strings.TrimSpace(corpo.SQL))
	if !strings.HasPrefix(inicio, "SELECT") && !strings.HasPrefix(inicio, "WITH") {
		responder(w, http.StatusBadRequest, erro("só SELECT"))
		return
	}
	s.rodar(w, r, "sql_livre", corpo.SQL, corpo.Params, s.cfg.API.MaxLinhas)
}

func (s *Servidor) atualizarAgora(w http.ResponseWriter, r *http.Request) {
	if err := os.WriteFile(s.cfg.Caminho(config.ArquivoAtualizarAgora), []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
		responder(w, http.StatusInternalServerError, erro(err.Error()))
		return
	}
	responder(w, http.StatusAccepted, map[string]any{"ok": true, "mensagem": "o agente vai procurar atualização em até 5 segundos"})
}

func (s *Servidor) rodar(w http.ResponseWriter, r *http.Request, nome, query string, args []any, max int) {
	ini := time.Now()
	res, err := s.banco.Consultar(r.Context(), query, args, max)
	ms := time.Since(ini).Milliseconds()
	if err != nil {
		s.erroBanco(w, nome, ms, err)
		return
	}
	responder(w, http.StatusOK, map[string]any{
		"consulta": nome,
		"total":    len(res.Linhas),
		"truncado": res.Truncado,
		"ms":       ms,
		"linhas":   res.Linhas,
	})
}

// foto serve a imagem de um produto: GET /foto/{codigo}/{seq}.jpg
//
// É pública (sem token) porque o WhatsApp precisa baixar a imagem por URL, e foto de
// catálogo não é dado sensível. O SQL vem da consulta foto_produto (resposta: imagem),
// que só devolve foto de produto ativo. Cache-Control deixa a Cloudflare guardar a
// imagem: o PC da loja só é consultado na primeira vez.
func (s *Servidor) foto(w http.ResponseWriter, r *http.Request) {
	cj := s.conjunto.Load()
	if cj == nil {
		http.Error(w, "indisponível", http.StatusServiceUnavailable)
		return
	}
	c, ok := cj.Consultas[consultaFoto]
	if !ok || c.Resposta != "imagem" {
		http.NotFound(w, r)
		return
	}
	arquivo := r.PathValue("arquivo")
	seq := strings.TrimSuffix(arquivo, path.Ext(arquivo))
	query, args, err := c.Montar(map[string]string{"codigo": r.PathValue("codigo"), "seq": seq})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ini := time.Now()
	img, err := s.banco.Binario(r.Context(), query, args)
	if err != nil {
		s.erroBanco(w, consultaFoto, time.Since(ini).Milliseconds(), err)
		return
	}
	if len(img) == 0 {
		http.NotFound(w, r)
		return
	}
	img = imagem.Reduzir(img)
	w.Header().Set("Content-Type", http.DetectContentType(img))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(img)
}

const consultaFoto = "foto_produto"

func (s *Servidor) erroBanco(w http.ResponseWriter, nome string, ms int64, err error) {
	s.log.Printf("ERRO consulta=%s ms=%d: %v", nome, ms, err)
	switch {
	case errors.Is(err, banco.ErrOcupado):
		responder(w, http.StatusServiceUnavailable, erro(err.Error()))
	case banco.ForaDoAr(err):
		responder(w, http.StatusServiceUnavailable, erro("banco do ERP fora do ar ou inacessível"))
	case errors.Is(err, context.DeadlineExceeded):
		responder(w, http.StatusGatewayTimeout, erro(fmt.Sprintf("consulta passou de %ds e foi cancelada", s.cfg.API.TimeoutSegundos)))
	default:
		responder(w, http.StatusInternalServerError, erro(err.Error()))
	}
}

// ---------- apoio ----------

func (s *Servidor) tokenOK(r *http.Request, esperado string) bool {
	if esperado == "" {
		return false
	}
	t := r.Header.Get("X-Token")
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		t = strings.TrimPrefix(a, "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(t), []byte(esperado)) == 1
}

func (s *Servidor) autenticado(admin bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		esperado := s.cfg.API.Token
		if admin {
			esperado = s.cfg.API.TokenAdmin
		}
		// token_admin também serve para as rotas normais
		if !s.tokenOK(r, esperado) && !s.tokenOK(r, s.cfg.API.TokenAdmin) {
			responder(w, http.StatusUnauthorized, erro("token inválido"))
			return
		}
		h(w, r)
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

// registrar loga cada requisição. Valores dos parâmetros (CPF, telefone) ficam de fora do log.
func (s *Servidor) registrar(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ini := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		h.ServeHTTP(sw, r)
		if r.URL.Path == "/health" && sw.status == 200 {
			return // não enche o log com o monitoramento
		}
		s.log.Printf("%s %s -> %d (%dms)", r.Method, r.URL.Path, sw.status, time.Since(ini).Milliseconds())
	})
}

func lerParams(r *http.Request) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range r.URL.Query() {
		out[k] = v[0]
	}
	if r.Method == http.MethodPost && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
		dec.UseNumber()
		var corpo map[string]any
		if err := dec.Decode(&corpo); err != nil {
			return nil, fmt.Errorf("JSON inválido: %w", err)
		}
		for k, v := range corpo {
			if v != nil {
				out[k] = fmt.Sprint(v)
			}
		}
	}
	return out, nil
}

func limite(daConsulta, global int) int {
	if daConsulta > 0 && daConsulta < global {
		return daConsulta
	}
	return global
}

func erro(msg string) map[string]any { return map[string]any{"erro": msg} }

func responder(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

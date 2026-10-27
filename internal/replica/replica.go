// Package replica copia as extrações do queries.yaml para o Supabase: lê o Firebird,
// compara o hash de cada linha com o que já foi enviado e manda só o que mudou para a
// Edge Function "fdc". O ERP nunca é alterado.
package replica

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"slices"
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

// atalho: a extração inteira fica na memória (clientes ~ dezenas de mil linhas). Acima de
// algumas centenas de mil, ler em streaming.
const maxLinhasExtracao = 1_000_000

// ErrContinua: a extração parou no meio de propósito (tempo das fotos esgotado) e deve
// voltar no próximo ciclo, não no próximo intervalo.
var ErrContinua = errors.New("continua no próximo ciclo")

type Replicador struct {
	cfg   *config.Config
	banco *banco.Banco
	sup   *Cliente
	log   *log.Logger

	// Tempo máximo de envio de fotos por ciclo (0 = sem limite). A primeira carga tem
	// centenas de fotos e não pode segurar produtos, estoque e parcelas.
	TempoFotos time.Duration

	conjunto atomic.Pointer[consultas.Conjunto]
	proxima  map[string]time.Time // só a goroutine de Rodar mexe

	mu       sync.Mutex
	yamlErro string
	erros    map[string]string // tabela -> erro do último ciclo
}

func Novo(cfg *config.Config, b *banco.Banco, l *log.Logger) *Replicador {
	return &Replicador{
		cfg:     cfg,
		banco:   b,
		sup:     NovoCliente(cfg.Supabase.URL, cfg.Supabase.Token),
		log:     l,
		proxima: map[string]time.Time{},
		erros:   map[string]string{},
	}
}

// Conjunto é o queries.yaml em uso (nil até o primeiro carregar com sucesso).
func (r *Replicador) Conjunto() *consultas.Conjunto { return r.conjunto.Load() }

// CarregarYAML relê o queries.yaml se ele mudou. Arquivo com erro não substitui o que
// funciona: o erro aparece no /health local e no worker_status do Supabase.
func (r *Replicador) CarregarYAML() {
	conteudo, err := os.ReadFile(r.cfg.Caminho("queries.yaml"))
	if err == nil {
		if atual := r.conjunto.Load(); atual != nil && atual.SHA256 == sha(conteudo) {
			return
		}
		var cj *consultas.Conjunto
		if cj, err = consultas.Carregar(conteudo); err == nil {
			r.conjunto.Store(cj)
			r.log.Printf("queries.yaml carregado: %d extrações, %d consultas (sha %.12s)", len(cj.Extracoes), len(cj.Consultas), cj.SHA256)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	novo := ""
	if err != nil {
		novo = err.Error()
	}
	if novo != r.yamlErro && novo != "" {
		r.log.Printf("ERRO queries.yaml (sigo com o anterior): %s", novo)
	}
	r.yamlErro = novo
}

// Saude devolve o sha do queries.yaml em uso e o erro do arquivo novo, se houver.
func (r *Replicador) Saude() (sha, erro string) {
	if cj := r.conjunto.Load(); cj != nil {
		sha = cj.SHA256
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return sha, r.yamlErro
}

// Rodar sincroniza cada extração no seu intervalo, atende os comandos da equipe a cada
// 30 s e manda sinal de vida a cada minuto. Uma coisa por vez, para não pesar no ERP.
func (r *Replicador) Rodar(ctx context.Context) {
	r.CarregarYAML()
	// O queries.yaml é relido à parte: a primeira carga de fotos leva minutos e o agente
	// precisa ver o arquivo novo aceito (consultas_sha no /health) em poucos segundos.
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.CarregarYAML()
			}
		}
	}()

	var proxComandos, proxStatus time.Time
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		if cj := r.conjunto.Load(); cj != nil {
			for _, e := range Ordem(cj) {
				if ctx.Err() != nil {
					return
				}
				if time.Now().Before(r.proxima[e.Tabela]) {
					continue
				}
				r.proxima[e.Tabela] = time.Now().Add(time.Duration(e.IntervaloMinutos) * time.Minute)
				err := r.Sincronizar(ctx, e)
				if errors.Is(err, ErrContinua) {
					r.proxima[e.Tabela], err = time.Time{}, nil
				}
				r.registrar(e.Tabela, err)
			}
		}
		if time.Now().After(proxComandos) {
			r.atenderComandos(ctx)
			proxComandos = time.Now().Add(30 * time.Second)
		}
		if time.Now().After(proxStatus) {
			r.enviarStatus(ctx)
			proxStatus = time.Now().Add(time.Minute)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Ordem das extrações: alfabética, com as fotos por último (são as mais demoradas).
func Ordem(cj *consultas.Conjunto) []*consultas.Extracao {
	es := slices.Collect(maps.Values(cj.Extracoes))
	slices.SortFunc(es, func(a, b *consultas.Extracao) int {
		if (a.Tipo == "foto") != (b.Tipo == "foto") {
			if a.Tipo == "foto" {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Tabela, b.Tabela)
	})
	return es
}

func (r *Replicador) registrar(tabela string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		delete(r.erros, tabela)
		return
	}
	r.erros[tabela] = err.Error()
	r.log.Printf("ERRO %s: %v", tabela, err)
}

// ---------- sincronização de uma tabela

type linha struct {
	dados map[string]any // colunas do SQL + chave + hash, como vão para o Supabase
	hash  string
}

type plano struct {
	atual     map[string]linha
	mudou     []string // chaves novas ou alteradas, em ordem
	remover   []string // chaves que estão no Supabase e não vieram mais do ERP
	repetidas int
}

// planejar compara o que veio do ERP com o que já está no Supabase (base: chave -> hash).
func planejar(e *consultas.Extracao, linhas []map[string]any, base map[string]string) (*plano, error) {
	p := &plano{atual: make(map[string]linha, len(linhas))}
	partes := make([]string, len(e.Chave))
	for _, dados := range linhas {
		for i, c := range e.Chave {
			v, ok := dados[c]
			if !ok {
				return nil, fmt.Errorf("a coluna %s da chave não está no SQL", c)
			}
			if v == nil {
				v = ""
			}
			partes[i] = fmt.Sprint(v)
		}
		k := strings.Join(partes, "|")
		if _, existe := p.atual[k]; existe {
			p.repetidas++
			continue
		}
		b, err := json.Marshal(dados) // encoding/json ordena as chaves: o hash é estável
		if err != nil {
			return nil, err
		}
		h := sha(b)[:16]
		dados["chave"], dados["hash"] = k, h
		p.atual[k] = linha{dados: dados, hash: h}
		if base[k] != h {
			p.mudou = append(p.mudou, k)
		}
	}
	for k := range base {
		if _, ok := p.atual[k]; !ok {
			p.remover = append(p.remover, k)
		}
	}
	slices.Sort(p.mudou)
	slices.Sort(p.remover)

	if len(p.remover) > 0 {
		if len(p.atual) == 0 {
			return nil, fmt.Errorf("o ERP devolveu 0 linhas e o Supabase tem %d: nada foi apagado (banco errado ou SQL quebrado?)", len(base))
		}
		limite := max(50, len(base)*e.MaxRemocaoPerc/100)
		if len(p.remover) > limite {
			return nil, fmt.Errorf("ia apagar %d de %d linhas (limite %d%%): nada foi enviado; confira o SQL e se o banco é o de produção",
				len(p.remover), len(base), e.MaxRemocaoPerc)
		}
	}
	return p, nil
}

// Sincronizar lê a extração inteira do ERP e manda a diferença para o Supabase.
func (r *Replicador) Sincronizar(ctx context.Context, e *consultas.Extracao) error {
	ini := time.Now()
	res, err := r.banco.Consultar(ctx, e.SQL, nil, maxLinhasExtracao)
	if err != nil {
		return fmt.Errorf("lendo o ERP: %w", err)
	}
	if res.Truncado {
		return fmt.Errorf("mais de %d linhas: filtre o SQL", maxLinhasExtracao)
	}

	dir := r.cfg.Caminho("estado")
	est, ok := lerEstado(dir, e.Tabela)
	if !ok || time.Since(est.Reconciliado) > time.Duration(r.cfg.Sincronizacao.ReconciliarHoras)*time.Hour {
		hs, err := r.sup.Hashes(ctx, e.Tabela)
		if err != nil {
			return fmt.Errorf("lendo o que já está no Supabase: %w", err)
		}
		est = &estado{Reconciliado: time.Now(), Hashes: hs}
	}

	p, err := planejar(e, res.Linhas, est.Hashes)
	if err != nil {
		return err
	}
	if p.repetidas > 0 {
		r.log.Printf("aviso %s: %d linhas com chave repetida no SQL (fica a primeira)", e.Tabela, p.repetidas)
	}

	var parcial error
	if e.Tipo == "foto" {
		parcial, err = r.enviarFotos(ctx, e, p, est, dir)
	} else {
		err = r.enviarLinhas(ctx, e, p, est)
	}
	if errSalvar := est.salvar(dir, e.Tabela); errSalvar != nil {
		r.log.Printf("aviso %s: não salvei o estado local: %v", e.Tabela, errSalvar)
	}
	if err != nil {
		return err
	}
	if err := r.sup.Concluir(ctx, e.Tabela, len(p.atual)); err != nil {
		return err
	}
	if len(p.mudou)+len(p.remover) > 0 {
		r.log.Printf("%s: %d linhas no ERP, %d enviadas, %d apagadas (%s)",
			e.Tabela, len(p.atual), len(p.mudou), len(p.remover), time.Since(ini).Round(time.Millisecond))
	}
	return parcial
}

// enviarLinhas manda em lotes e marca no estado só o que o Supabase confirmou: se cair no
// meio, o próximo ciclo continua de onde parou.
func (r *Replicador) enviarLinhas(ctx context.Context, e *consultas.Extracao, p *plano, est *estado) error {
	lote := r.cfg.Sincronizacao.Lote
	for i := 0; i < len(p.mudou); i += lote {
		chaves := p.mudou[i:min(i+lote, len(p.mudou))]
		linhas := make([]map[string]any, len(chaves))
		for j, k := range chaves {
			linhas[j] = p.atual[k].dados
		}
		if err := r.sup.Aplicar(ctx, e.Tabela, linhas, nil); err != nil {
			return err
		}
		for _, k := range chaves {
			est.Hashes[k] = p.atual[k].hash
		}
	}
	for i := 0; i < len(p.remover); i += 2000 {
		chaves := p.remover[i:min(i+2000, len(p.remover))]
		if err := r.sup.Aplicar(ctx, e.Tabela, nil, chaves); err != nil {
			return err
		}
		for _, k := range chaves {
			delete(est.Hashes, k)
		}
	}
	return nil
}

// enviarFotos busca cada imagem nova ou trocada, reduz se for grande e sobe uma por vez.
// Foto que falha não trava as outras: volta como erro parcial e é tentada no próximo ciclo.
func (r *Replicador) enviarFotos(ctx context.Context, e *consultas.Extracao, p *plano, est *estado, dir string) (parcial, fatal error) {
	falhas := 0
	var ultima error
	ini := time.Now()
	for n, k := range p.mudou {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if r.TempoFotos > 0 && time.Since(ini) > r.TempoFotos {
			r.log.Printf("%s: %d de %d fotos enviadas neste ciclo, o resto vai no próximo", e.Tabela, n, len(p.mudou))
			return nil, ErrContinua
		}
		l := p.atual[k]
		codigo, seq := fmt.Sprint(l.dados["codigo"]), fmt.Sprint(l.dados["seq"])
		q, args, err := e.Arquivo.Montar(map[string]string{"codigo": codigo, "seq": seq})
		if err != nil {
			return nil, err
		}
		img, err := r.banco.Binario(ctx, q, args)
		if err != nil {
			return nil, fmt.Errorf("lendo a foto %s no ERP: %w", k, err)
		}
		if len(img) == 0 {
			continue // apagada entre a listagem e a leitura
		}
		if err := r.sup.EnviarFoto(ctx, codigo, seq, l.hash, imagem.Reduzir(img)); err != nil {
			falhas, ultima = falhas+1, err
			continue
		}
		est.Hashes[k] = l.hash
		if n%50 == 49 { // a primeira carga tem centenas de fotos: guarda o progresso
			est.salvar(dir, e.Tabela)
		}
	}
	for i := 0; i < len(p.remover); i += 500 {
		chaves := p.remover[i:min(i+500, len(p.remover))]
		if err := r.sup.RemoverFotos(ctx, chaves); err != nil {
			return nil, err
		}
		for _, k := range chaves {
			delete(est.Hashes, k)
		}
	}
	if falhas > 0 {
		return fmt.Errorf("%d foto(s) não enviadas; última: %w", falhas, ultima), nil
	}
	return nil, nil
}

// ---------- comandos da equipe e sinal de vida

func (r *Replicador) atenderComandos(ctx context.Context) {
	cs, err := r.sup.Comandos(ctx)
	if err != nil {
		r.log.Printf("comandos: %v", err)
		return
	}
	for _, c := range cs {
		r.log.Printf("comando %v: %s %s", c.ID, c.Tipo, c.Consulta)
		res, err := r.executarComando(ctx, c)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if err := r.sup.Responder(ctx, c.ID, res, msg); err != nil {
			r.log.Printf("comando %v: não consegui responder: %v", c.ID, err)
		}
	}
}

func (r *Replicador) executarComando(ctx context.Context, c Comando) (any, error) {
	switch c.Tipo {
	case "atualizar":
		return map[string]any{"ok": true}, os.WriteFile(r.cfg.Caminho(config.ArquivoAtualizarAgora), nil, 0o644)
	case "consulta":
		cj := r.conjunto.Load()
		if cj == nil {
			return nil, fmt.Errorf("queries.yaml não carregado")
		}
		q, ok := cj.Consultas[c.Consulta]
		if !ok {
			return nil, fmt.Errorf("consulta %q não existe no queries.yaml", c.Consulta)
		}
		params := map[string]string{}
		for k, v := range c.Params {
			params[k] = fmt.Sprint(v)
		}
		query, args, err := q.Montar(params)
		if err != nil {
			return nil, err
		}
		limite := q.MaxLinhas
		if limite == 0 {
			limite = 200
		}
		return r.banco.Consultar(ctx, query, args, limite)
	}
	return nil, fmt.Errorf("tipo de comando desconhecido: %s", c.Tipo)
}

func (r *Replicador) enviarStatus(ctx context.Context) {
	shaYAML, erroYAML := r.Saude()
	r.mu.Lock()
	erros := maps.Clone(r.erros)
	r.mu.Unlock()
	s := map[string]any{
		"versao":    versao.Versao,
		"banco_ok":  r.banco.Ping(ctx) == nil,
		"yaml_sha":  shaYAML,
		"yaml_erro": erroYAML,
		"erros":     erros,
	}
	if err := r.sup.Status(ctx, s); err != nil {
		r.log.Printf("sinal de vida para o Supabase: %v", err)
	}
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

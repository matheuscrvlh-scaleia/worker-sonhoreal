// worker-fdc: copia o ERP FDC (Firebird) da loja para o Supabase, onde a IA consulta.
// Só lê o ERP. Se o PC desligar, a IA continua com a última cópia.
//
//	worker-fdc.exe                  sincroniza sem parar (normalmente quem inicia é o worker-fdc-agente)
//	worker-fdc.exe -testar          testa banco, Supabase, cada extração e cada diag do queries.yaml
//	worker-fdc.exe -uma-vez         sincroniza todas as extrações uma vez e sai (primeira carga)
//	worker-fdc.exe -config X.json   usa outro config
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"ponte/internal/banco"
	"ponte/internal/config"
	"ponte/internal/consultas"
	"ponte/internal/replica"
	"ponte/internal/versao"
)

func main() {
	exe, _ := os.Executable()
	caminhoCfg := flag.String("config", filepath.Join(filepath.Dir(exe), "config.json"), "caminho do config.json")
	testar := flag.Bool("testar", false, "testa banco, Supabase e queries.yaml e sai")
	queriesTeste := flag.String("queries", "", "com -testar: testa este queries.yaml em vez do que está ao lado do config")
	umaVez := flag.Bool("uma-vez", false, "sincroniza tudo uma vez e sai")
	flag.Parse()

	cfg, err := config.Carregar(*caminhoCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERRO:", err)
		os.Exit(2)
	}

	// 2 conexões: uma extração por vez, mais um comando de diagnóstico se chegar no meio.
	b, err := banco.Abrir(cfg.Banco.DSN(), 2, time.Duration(cfg.Sincronizacao.TimeoutSegundos)*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERRO abrindo banco:", err)
		os.Exit(2)
	}
	defer b.Fechar()

	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer parar()

	if *testar {
		caminho := *queriesTeste
		if caminho == "" {
			caminho = cfg.Caminho("queries.yaml")
		}
		os.Exit(rodarTestes(ctx, cfg, b, caminho))
	}

	arquivoLog := &lumberjack.Logger{Filename: cfg.Caminho(filepath.Join("logs", "worker.log")), MaxSize: 10, MaxBackups: 5, MaxAge: 30}
	l := log.New(io.MultiWriter(os.Stdout, arquivoLog), "", log.LstdFlags)
	l.Printf("worker-fdc %s iniciando (cliente %s, Supabase %s)", versao.Versao, cfg.Cliente, cfg.Supabase.URL)

	r := replica.Novo(cfg, b, l)
	if *umaVez {
		os.Exit(sincronizarUmaVez(ctx, r, l))
	}
	r.TempoFotos = 3 * time.Minute

	srv := &http.Server{Addr: cfg.Local.Escutar, Handler: saude(cfg, b, r), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Sem o /health o agente não consegue conferir atualizações: melhor cair e o supervisor tentar de novo.
			l.Printf("ERRO /health local em %s: %v", cfg.Local.Escutar, err)
			os.Exit(1)
		}
	}()
	r.Rodar(ctx)
}

// saude é o GET /health só da máquina (127.0.0.1): o agente usa para conferir uma versão
// nova e o queries.yaml novo. Nada aqui é exposto para fora.
func saude(cfg *config.Config, b *banco.Banco, r *replica.Replicador) http.Handler {
	inicio := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, req *http.Request) {
		errBanco := b.Ping(req.Context())
		sha, erroYAML := r.Saude()
		status := http.StatusOK
		if errBanco != nil {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"ok":%t,"versao":%q,"cliente":%q,"banco_ok":%t,"consultas_sha":%q,"consultas_erro":%q,"no_ar_desde":%q}`,
			errBanco == nil && erroYAML == "", versao.Versao, cfg.Cliente, errBanco == nil, sha, erroYAML, inicio.Format(time.RFC3339))
	})
	return mux
}

func sincronizarUmaVez(ctx context.Context, r *replica.Replicador, l *log.Logger) int {
	r.CarregarYAML()
	cj := r.Conjunto()
	if cj == nil {
		_, erro := r.Saude()
		l.Printf("ERRO queries.yaml: %s", erro)
		return 1
	}
	falhas := 0
	for _, e := range replica.Ordem(cj) {
		ini := time.Now()
		if err := r.Sincronizar(ctx, e); err != nil {
			falhas++
			l.Printf("FALHOU %s: %v", e.Tabela, err)
			continue
		}
		l.Printf("OK %s (%s)", e.Tabela, time.Since(ini).Round(time.Millisecond))
	}
	if falhas > 0 {
		return 1
	}
	return 0
}

// rodarTestes valida a instalação no banco real: conexão, Supabase, cada extração (colunas
// da chave, quantidade de linhas) e cada diagnóstico com os exemplos. Não envia nada.
func rodarTestes(ctx context.Context, cfg *config.Config, b *banco.Banco, caminhoQueries string) int {
	falhas := 0

	fmt.Printf("Conectando em %s:%d %s ... ", cfg.Banco.Host, cfg.Banco.Porta, cfg.Banco.Arquivo)
	if err := b.Ping(ctx); err != nil {
		fmt.Println("FALHOU:", err)
		return 1
	}
	fmt.Println("OK")

	res, err := b.Consultar(ctx, `SELECT rdb$get_context('SYSTEM','ENGINE_VERSION') AS versao,
		(SELECT RDB$CHARACTER_SET_NAME FROM RDB$DATABASE) AS charset FROM RDB$DATABASE`, nil, 1)
	if err == nil && len(res.Linhas) == 1 {
		fmt.Printf("Firebird %v, charset do banco %v (conexão em %s)\n", res.Linhas[0]["versao"], res.Linhas[0]["charset"], cfg.Banco.Charset)
	}

	fmt.Printf("Supabase %s ... ", cfg.Supabase.URL)
	if err := replica.NovoCliente(cfg.Supabase.URL, cfg.Supabase.Token).Ping(ctx); err != nil {
		falhas++
		fmt.Println("FALHOU:", err)
	} else {
		fmt.Println("OK")
	}

	conteudo, err := os.ReadFile(caminhoQueries)
	if err != nil {
		fmt.Println("queries.yaml:", err)
		return 1
	}
	cj, err := consultas.Carregar(conteudo)
	if err != nil {
		fmt.Println("queries.yaml inválido:", err)
		return 1
	}
	fmt.Printf("queries.yaml: %d extrações, %d consultas de diagnóstico\n\nExtrações (lê a tabela inteira):\n", len(cj.Extracoes), len(cj.Consultas))

	for _, nome := range slices.Sorted(maps.Keys(cj.Extracoes)) {
		e := cj.Extracoes[nome]
		ini := time.Now()
		r, err := b.Consultar(ctx, e.SQL, nil, 1_000_000)
		if err == nil {
			for _, c := range e.Chave {
				if !slices.Contains(r.Colunas, c) {
					err = fmt.Errorf("a coluna %s da chave não está no SQL", c)
				}
			}
		}
		if err == nil && e.Tipo == "foto" && len(r.Linhas) > 0 {
			q, args, _ := e.Arquivo.Montar(map[string]string{"codigo": fmt.Sprint(r.Linhas[0]["codigo"]), "seq": fmt.Sprint(r.Linhas[0]["seq"])})
			var img []byte
			if img, err = b.Binario(ctx, q, args); err == nil {
				fmt.Printf("         (primeira foto: %d bytes, %s)\n", len(img), http.DetectContentType(img))
			}
		}
		if err != nil {
			falhas++
			fmt.Printf("  FALHOU %-28s %v\n", nome, err)
			continue
		}
		fmt.Printf("  OK     %-28s %5dms  %d linha(s)\n", nome, time.Since(ini).Milliseconds(), len(r.Linhas))
	}

	fmt.Println("\nDiagnósticos:")
	for _, nome := range slices.Sorted(maps.Keys(cj.Consultas)) {
		c := cj.Consultas[nome]
		params := map[string]string{}
		for _, p := range c.Params {
			if p.Exemplo != "" {
				params[p.Nome] = p.Exemplo
			}
		}
		query, args, err := c.Montar(params)
		if err == nil {
			ini := time.Now()
			var r *banco.Resultado
			if r, err = b.Consultar(ctx, query, args, 3); err == nil {
				fmt.Printf("  OK     %-28s %5dms  %d linha(s)\n", nome, time.Since(ini).Milliseconds(), len(r.Linhas))
				continue
			}
		}
		falhas++
		fmt.Printf("  FALHOU %-28s %v\n", nome, err)
	}
	if falhas > 0 {
		fmt.Printf("\n%d item(ns) com erro\n", falhas)
		return 1
	}
	fmt.Println("\nTudo certo.")
	return 0
}

// ponte-api: API HTTP local que consulta o Firebird do ERP.
//
//	ponte-api.exe                  sobe a API (normalmente quem inicia é o ponte-agente)
//	ponte-api.exe -testar          testa conexão e roda cada consulta com os exemplos do queries.yaml
//	ponte-api.exe -config X.json   usa outro config
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

	"ponte/internal/api"
	"ponte/internal/banco"
	"ponte/internal/config"
	"ponte/internal/consultas"
	"ponte/internal/versao"
)

func main() {
	exe, _ := os.Executable()
	caminhoCfg := flag.String("config", filepath.Join(filepath.Dir(exe), "config.json"), "caminho do config.json")
	testar := flag.Bool("testar", false, "testa banco e consultas e sai")
	flag.Parse()

	cfg, err := config.Carregar(*caminhoCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERRO:", err)
		os.Exit(2)
	}

	b, err := banco.Abrir(cfg.Banco.DSN(), cfg.API.MaxSimultaneas, time.Duration(cfg.API.TimeoutSegundos)*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERRO abrindo banco:", err)
		os.Exit(2)
	}
	defer b.Fechar()

	if *testar {
		os.Exit(rodarTestes(cfg, b))
	}

	arquivoLog := &lumberjack.Logger{Filename: cfg.Caminho(filepath.Join("logs", "api.log")), MaxSize: 10, MaxBackups: 5, MaxAge: 30}
	l := log.New(io.MultiWriter(os.Stdout, arquivoLog), "", log.LstdFlags)
	l.Printf("ponte-api %s iniciando (cliente %s, escutando %s)", versao.Versao, cfg.Cliente, cfg.API.Escutar)

	srv := api.Novo(cfg, b, l)
	srv.CarregarConsultas() // se falhar, sobe mesmo assim: /health mostra o erro

	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer parar()
	go srv.VigiarConsultas(ctx)

	httpSrv := &http.Server{
		Addr:              cfg.API.Escutar,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      time.Duration(cfg.API.TimeoutSegundos+15) * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(c)
	}()
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		l.Printf("ERRO servidor HTTP: %v", err)
		os.Exit(1)
	}
}

// rodarTestes valida a instalação: conexão, versão do Firebird e cada consulta
// executada com os exemplos. Pega coluna errada, tipo errado, etc. no banco real.
func rodarTestes(cfg *config.Config, b *banco.Banco) int {
	ctx := context.Background()
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

	conteudo, err := os.ReadFile(cfg.Caminho("queries.yaml"))
	if err != nil {
		fmt.Println("queries.yaml:", err)
		return 1
	}
	cj, err := consultas.Carregar(conteudo)
	if err != nil {
		fmt.Println("queries.yaml inválido:", err)
		return 1
	}
	fmt.Printf("queries.yaml: %d consultas\n\n", len(cj.Consultas))

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
				fmt.Printf("  OK     %-28s %4dms  %d linha(s)\n", nome, time.Since(ini).Milliseconds(), len(r.Linhas))
				continue
			}
		}
		falhas++
		fmt.Printf("  FALHOU %-28s %v\n", nome, err)
	}
	if falhas > 0 {
		fmt.Printf("\n%d consulta(s) com erro\n", falhas)
		return 1
	}
	fmt.Println("\nTudo certo.")
	return 0
}

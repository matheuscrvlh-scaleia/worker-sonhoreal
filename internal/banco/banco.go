// Package banco executa as consultas no Firebird sempre em transação curta,
// somente leitura, com timeout e limite de consultas simultâneas, para nunca
// pesar no ERP do cliente.
package banco

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/nakagami/firebirdsql"
	"golang.org/x/text/encoding/charmap"
)

type Banco struct {
	db      *sql.DB
	vagas   chan struct{}
	timeout time.Duration
}

var ErrOcupado = errors.New("banco ocupado: muitas consultas ao mesmo tempo")

func Abrir(dsn string, maxSimultaneas int, timeout time.Duration) (*Banco, error) {
	// Se o servidor não atender o cancelamento de uma consulta que estourou o tempo, derruba o socket.
	if !strings.Contains(dsn, "cancel_hard_drop") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn += sep + "cancel_hard_drop=true"
	}
	db, err := sql.Open("firebirdsql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxSimultaneas)
	db.SetMaxIdleConns(2)
	// Conexões renovadas com frequência: se o ERP/servidor reiniciar, nenhuma fica pendurada.
	db.SetConnMaxLifetime(10 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	return &Banco{db: db, vagas: make(chan struct{}, maxSimultaneas), timeout: timeout}, nil
}

func (b *Banco) Fechar() error { return b.db.Close() }

func (b *Banco) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return b.db.PingContext(ctx)
}

type Resultado struct {
	Colunas  []string         `json:"colunas"`
	Linhas   []map[string]any `json:"linhas"`
	Truncado bool             `json:"truncado"`
}

func (b *Banco) Consultar(ctx context.Context, query string, args []any, maxLinhas int) (*Resultado, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	select {
	case b.vagas <- struct{}{}:
		defer func() { <-b.vagas }()
	case <-ctx.Done():
		return nil, ErrOcupado
	}

	tx, err := b.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("abrindo transação: %w", err)
	}
	// Somente leitura: rollback e commit têm o mesmo efeito; rollback é o mais seguro.
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	for i := range cols {
		cols[i] = strings.ToLower(cols[i])
	}
	res := &Resultado{Colunas: cols, Linhas: []map[string]any{}}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if len(res.Linhas) == maxLinhas {
			res.Truncado = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		linha := make(map[string]any, len(cols))
		for i, c := range cols {
			linha[c] = limpar(vals[i])
		}
		res.Linhas = append(res.Linhas, linha)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

// limpar deixa o valor pronto para JSON: tira o preenchimento de CHAR,
// formata datas e corta o ruído de DOUBLE (1299.8999999 -> 1299.9).
func limpar(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return strings.TrimRight(x, " ")
	case []byte: // BLOB de texto vem cru, sem conversão de charset
		if !utf8.Valid(x) {
			if d, err := charmap.Windows1252.NewDecoder().Bytes(x); err == nil {
				x = d
			}
		}
		return strings.TrimRight(string(x), " ")
	case time.Time:
		if x.Hour() == 0 && x.Minute() == 0 && x.Second() == 0 {
			return x.Format("2006-01-02")
		}
		return x.Format("2006-01-02 15:04:05")
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		return math.Round(x*10000) / 10000
	case float32:
		return limpar(float64(x))
	}
	if s, ok := v.(fmt.Stringer); ok { // NUMERIC/DECIMAL vêm como tipo decimal do driver
		return s.String()
	}
	return v
}

package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

func TestTranslateClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want error // nil means "neither unavailable, not found nor conflict"
	}{
		{"no rows", pgx.ErrNoRows, repository.ErrNotFound},
		{"unique violation", &pgconn.PgError{Code: "23505"}, repository.ErrConflict},
		{"foreign key", &pgconn.PgError{Code: "23503"}, nil},
		{"check violation", &pgconn.PgError{Code: "23514"}, nil},
		{"syntax error", &pgconn.PgError{Code: "42601"}, nil},
		{"connection failure", &pgconn.PgError{Code: "08006"}, repository.ErrUnavailable},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, repository.ErrUnavailable},
		{"cannot connect now", &pgconn.PgError{Code: "57P03"}, repository.ErrUnavailable},
		{"too many connections", &pgconn.PgError{Code: "53300"}, repository.ErrUnavailable},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), repository.ErrUnavailable},
		{"network", &net.OpError{Op: "read", Err: errors.New("connection reset")}, repository.ErrUnavailable},
		{"unexpected eof", fmt.Errorf("receive: %w", io.ErrUnexpectedEOF), repository.ErrUnavailable},
		{"caller cancelled", context.Canceled, nil},
		{"unknown", errors.New("something else"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translate("op", tc.err)
			for _, sentinel := range []error{repository.ErrUnavailable, repository.ErrNotFound, repository.ErrConflict} {
				if errors.Is(got, sentinel) != (sentinel == tc.want) {
					t.Fatalf("translate(%v) = %v; errors.Is(%v) mismatch", tc.err, got, sentinel)
				}
			}
		})
	}
}

func TestTranslateDropsServerMessageAndDetail(t *testing.T) {
	err := translate("op", &pgconn.PgError{Code: "23514", Message: "row value SECRET-ROW-VALUE", Detail: "Failing row contains (SECRET-ROW-VALUE)"})
	if strings.Contains(err.Error(), "SECRET-ROW-VALUE") {
		t.Fatalf("error leaked server detail: %v", err)
	}
}

// deadPool returns a pool whose connections can never be established. pgx
// connects lazily, so creating it succeeds and queries fail like an outage.
func deadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close() // nothing listens on this port any more
	config, err := pgxpool.ParseConfig("postgres://voxa:DEAD-POOL-PASSWORD@" + address + "/voxa?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestRealOutagesClassifyAsUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, _, err := NewMeetingRepository(deadPool(t)).List(ctx, "ws", nil, 10)
	if !errors.Is(err, repository.ErrUnavailable) {
		t.Fatalf("refused connection err = %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), "DEAD-POOL-PASSWORD") || strings.Contains(err.Error(), "user=") {
		t.Fatalf("error leaked connection details: %v", err)
	}

	closed := deadPool(t)
	closed.Close()
	if _, err := NewConfigurationRepository(closed, configurationDefaultsForTest).Get(ctx, "ws"); !errors.Is(err, repository.ErrUnavailable) {
		t.Fatalf("closed pool err = %v, want ErrUnavailable", err)
	}
}

var configurationDefaultsForTest = domain.Configuration{}

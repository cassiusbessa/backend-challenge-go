package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

// Server escuta a API e o pprof em endereços diferentes.
type Server struct {
	api       *http.Server
	pprof     *http.Server
	apiAddr   string
	listening chan struct{}
	log       *slog.Logger
}

// NewServer guarda os endereços. A escuta começa em Start.
func NewServer(cfg config.Config, handler http.Handler, logger *slog.Logger) *Server {
	return &Server{
		api: &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           handler,
			ReadHeaderTimeout: time.Second,
		},
		pprof: &http.Server{
			Addr:              cfg.PPROFAddr,
			Handler:           pprofHandler(),
			ReadHeaderTimeout: time.Second,
		},
		listening: make(chan struct{}),
		log:       logger,
	}
}

// Start deixa de retornar só depois que as duas portas aceitam conexão.
func (s *Server) Start(ctx context.Context) error {
	if err := s.listen(ctx, s.pprof, nil); err != nil {
		return err
	}
	if err := s.listen(ctx, s.api, &s.apiAddr); err != nil {
		return err
	}
	close(s.listening)
	return nil
}

// Use troca o handler da API antes da escuta.
func (s *Server) Use(handler http.Handler) {
	s.api.Handler = handler
}

// Addr é o endereço efetivo da API, depois do Start.
func (s *Server) Addr() string {
	return s.apiAddr
}

// Listening fecha quando a API está aceitando conexão.
func (s *Server) Listening() <-chan struct{} {
	return s.listening
}

// Shutdown para de aceitar conexão e conclui o pedido em curso.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.api.Shutdown(ctx); err != nil {
		return err
	}
	return s.pprof.Shutdown(ctx)
}

func (s *Server) listen(ctx context.Context, srv *http.Server, addr *string) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		return err
	}
	if addr != nil {
		*addr = ln.Addr().String()
	}
	go s.serve(srv, ln)
	return nil
}

func (s *Server) serve(srv *http.Server, ln net.Listener) {
	err := srv.Serve(ln)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.log.Error("servidor http", slog.String("status", "error"))
	}
}

func pprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

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

type Server struct {
	api       *http.Server
	pprof     *http.Server
	apiAddr   string
	listening chan struct{}
	log       *slog.Logger
}

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

func (s *Server) Use(handler http.Handler) {
	s.api.Handler = handler
}

func (s *Server) Addr() string {
	return s.apiAddr
}

func (s *Server) Listening() <-chan struct{} {
	return s.listening
}

func (s *Server) Shutdown(ctx context.Context) error {
	return errors.Join(s.api.Shutdown(ctx), s.pprof.Shutdown(ctx))
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
		s.log.Error("http server", slog.String("status", "error"))
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

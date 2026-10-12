// Command blocky-ctrl serves a small remote control for blocky DNS instances.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"blocky-ctrl/internal/config"
	"blocky-ctrl/internal/server"
	"blocky-ctrl/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("blocky-ctrl", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "blocky-ctrl.yml", "config `file`")
	bind := flag.String("bind", "", "`address` to bind to (default: all interfaces)")
	port := flag.Int("port", 8080, "`port` to listen on")
	showVersion := flag.Bool("version", false, "print the version and exit")
	alias("c", "config")
	alias("b", "bind")
	alias("p", "port")
	alias("v", "version")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if flag.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", flag.Arg(0))
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	srv, err := server.New(cfg, web.FS, version)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(*bind, strconv.Itoa(*port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	hs := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()

	for _, in := range cfg.Instances {
		slog.Info("instance", "name", in.Name, "readonly", in.ReadOnly)
	}
	slog.Info("listening", "addr", ln.Addr().String(), "version", version)
	if err := hs.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// shorts maps long flag names to their one-letter aliases.
var shorts = map[string]string{}

// alias registers short as another name for the long flag.
func alias(short, long string) {
	f := flag.Lookup(long)
	flag.Var(f.Value, short, f.Usage)
	shorts[long] = short
}

// usage prints the flags GNU style: one dash for short names, two for long ones.
func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, "Usage: %s [options]\n\n", os.Args[0])
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	flag.VisitAll(func(f *flag.Flag) {
		if len(f.Name) == 1 {
			return
		}
		names := "    --" + f.Name
		if s, ok := shorts[f.Name]; ok {
			names = "-" + s + ", --" + f.Name
		}
		arg, help := flag.UnquoteUsage(f)
		if arg != "" {
			names += " <" + arg + ">"
		}
		switch _, err := strconv.Atoi(f.DefValue); {
		case f.DefValue == "" || f.DefValue == "false":
		case err == nil:
			help += " (default " + f.DefValue + ")"
		default:
			help += fmt.Sprintf(" (default %q)", f.DefValue)
		}
		fmt.Fprintf(tw, "  %s\t%s\n", names, help)
	})
	fmt.Fprintf(tw, "  -h, --help\tshow this help\n")
	tw.Flush()
}

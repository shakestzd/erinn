package daemon

import (
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/internal/launcher/mode"
	"github.com/spf13/cobra"
)

func NewServeCommand(run func(bind string, port int) error) *cobra.Command {
	var port int
	var bind string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start HTTP dashboard server with SSE event stream",
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(bind, port)
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 8080, "Port to listen on")
	cmd.Flags().StringVar(&bind, "bind", "127.0.0.1", "Bind address (use 0.0.0.0 when publishing port from a container)")
	return cmd
}

func ResolveDashboardAddress(isDevcontainer bool, getenv func(string) string) (string, int) {
	runtime := mode.RuntimeHost
	if isDevcontainer {
		runtime = mode.RuntimeDevcontainer
	}
	host, port := mode.DashboardBindDefaults(runtime)
	if v := getenv("WIPNOTE_SERVE_BIND"); v != "" {
		host = v
	}
	if v := getenv("WIPNOTE_SERVE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			port = p
		}
	}
	return host, port
}

func IsValidProjectID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, "/\\\x00") {
		return false
	}
	if len(id) < 4 {
		return false
	}
	for _, ch := range id {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func ParseProjectProxyPath(urlPath string) (projectID, remainder string, ok bool) {
	rest := strings.TrimPrefix(urlPath, "/p/")
	if i := strings.Index(rest, "/"); i >= 0 {
		projectID = rest[:i]
		remainder = rest[i:]
	} else {
		projectID = rest
		remainder = "/"
	}
	if !IsValidProjectID(projectID) {
		return "", "", false
	}
	return projectID, remainder, true
}

func ProbePort(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

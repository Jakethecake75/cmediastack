package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// The container is given longer to stop than the application takes to, or a
// stop kills it before it has written what it holds only in memory — the
// count of denials over the audit log's ceiling (ADR-0031). Docker's default
// is ten seconds; the application waits up to server.shutdown_grace first.
func TestTheContainerIsGivenLongerToStopThanTheApplicationTakes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^    stop_grace_period: (\S+)$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("docker-compose.yml sets no stop_grace_period for the application: Docker kills it after 10s")
	}
	d, err := time.ParseDuration(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	if grace := Default().Server.ShutdownGrace; d < grace+5*time.Second {
		t.Fatalf("stop_grace_period %v leaves no room after shutdown_grace %v", d, grace)
	}
}

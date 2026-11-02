// Package mongotest provides a shared MongoDB Testcontainers lifecycle for
// integration-test packages.
package mongotest

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
)

// mongoImage stays on the 8.2 line because mongo:8 (8.3) and mongo:8.0 refuse to
// start on Linux kernel 6.19 and newer (SERVER-121912), which CI runners use.
const mongoImage = "mongo:8.2"

// Manager lazily starts one MongoDB container for a test package and stops it
// after that package's tests finish. MONGODB_TEST_URL bypasses Testcontainers.
type Manager struct {
	healthOnce  sync.Once
	healthErr   error
	warningOnce sync.Once
	startOnce   sync.Once
	container   *mongodb.MongoDBContainer
	uri         string
	startErr    error
	healthCheck func() error
	warningOut  io.Writer
}

// URI returns the configured external MongoDB URL or lazily provisions a
// MongoDB 8.2 container. Tests are skipped with a warning when Docker is absent.
func (m *Manager) URI(t *testing.T) string {
	t.Helper()
	if uri := strings.TrimSpace(os.Getenv("MONGODB_TEST_URL")); uri != "" {
		return uri
	}

	m.healthOnce.Do(func() {
		healthCheck := m.healthCheck
		if healthCheck == nil {
			healthCheck = dockerHealthError
		}
		m.healthErr = healthCheck()
	})
	if m.healthErr != nil {
		warning := fmt.Sprintf("WARNING: Docker is not available; MongoDB integration tests skipped: %v", m.healthErr)
		m.warningOnce.Do(func() {
			warningOut := m.warningOut
			if warningOut == nil {
				warningOut = os.Stderr
			}
			_, _ = fmt.Fprintln(warningOut, warning)
		})
		t.Skip(warning)
	}

	m.startOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		m.container, m.startErr = mongodb.Run(ctx, mongoImage)
		if m.startErr != nil {
			return
		}
		m.uri, m.startErr = m.container.ConnectionString(ctx)
	})
	if m.startErr != nil {
		t.Fatalf("start MongoDB Testcontainer: %v", m.startErr)
	}
	return m.uri
}

// Run executes a package test suite and terminates any container started by
// URI. A cleanup failure turns an otherwise successful suite into a failure.
func (m *Manager) Run(tests *testing.M) int {
	code := tests.Run()
	if m.container == nil {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := testcontainers.TerminateContainer(m.container, testcontainers.StopContext(ctx)); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ERROR: terminate MongoDB Testcontainer: %v\n", err)
		if code == 0 {
			return 1
		}
	}
	return code
}

func dockerHealthError() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("docker provider panic: %v", recovered)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return err
	}
	return provider.Health(ctx)
}

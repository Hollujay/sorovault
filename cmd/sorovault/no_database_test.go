package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testContractID is a real-shaped testnet contract ID, so argument
// validation accepts it and the command reaches the point where it would
// need configuration.
const testContractID = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"

// testDatabaseURL is the connection string shape every test here uses. It
// looks like a credential and is not one.
const testDatabaseURL = "postgres://sorovault:sorovault@localhost:5432/sorovault" //nolint:gosec // fixture, not a real credential

// countListener is a TCP listener that records every connection that reaches
// it. Running a command with RPC_URL pointed at one turns "it never tried to
// connect" into something a test can assert instead of assume.
type countListener struct {
	net.Listener
	accepted atomic.Int64
}

// newCountListener starts listening on a loopback port and returns it
// together with the URL to point RPC_URL at.
func newCountListener(t *testing.T) (*countListener, string) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	counted := &countListener{Listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return // the listener was closed; there is nothing left to count
			}
			counted.accepted.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })

	return counted, "http://" + listener.Addr().String()
}

// commandRun is what one execution of the command tree did.
type commandRun struct {
	// output is everything the command printed.
	output string
	// err is how the command ended; nil when it succeeded.
	err error
	// rpc is the listener RPC_URL was pointed at.
	rpc *countListener
	// elapsed is how long the command took.
	elapsed time.Duration
}

// runWithoutDatabase executes the whole command tree the way the binary
// would, with no database configured and RPC_URL pointed at a listener that
// counts connections.
//
// It cannot be used from a parallel test: t.Setenv changes the process
// environment, which every test in this package shares.
func runWithoutDatabase(t *testing.T, args ...string) commandRun {
	t.Helper()

	// Both are cleared: DATABASE_URL_FILE is an equal way to configure the
	// connection string, and neither the operator's values nor a secret file
	// are present here.
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", "")

	rpc, rpcURL := newCountListener(t)
	t.Setenv("RPC_URL", rpcURL)

	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)

	start := time.Now()
	err := root.Execute()

	return commandRun{output: out.String(), err: err, rpc: rpc, elapsed: time.Since(start)}
}

// TestCommandsWithoutADatabaseFailFast is the issue's "done" list: every
// command that needs the database, run with DATABASE_URL unset, fails
// immediately with a message naming the variable — and none of them reaches
// RPC on the way.
//
// The commands the issue names (add, refresh, list, get, codegen, migrate and
// serve) are here, and so is every other command that needs the database:
// "every command except codegen --help and version" is the property being
// pinned, and the ones that share a code path (withApp, withStore,
// doctorDepsFromEnv) cost nothing extra to cover.
func TestCommandsWithoutADatabaseFailFast(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot.json")
	require.NoError(t, os.WriteFile(snapshot, []byte("{}"), 0o600))

	tests := []struct {
		name string
		args []string
	}{
		{"add", []string{"add", testContractID}},
		{"remove", []string{"remove", testContractID}},
		{"list", []string{"list"}},
		{"get", []string{"get", testContractID}},
		{"versions", []string{"versions", testContractID}},
		{"codegen", []string{"codegen", testContractID}},
		{"refresh", []string{"refresh", testContractID}},
		{"export", []string{"export"}},
		// import is given a file, so it reads a snapshot rather than
		// empty stdin: the point is that it gets as far as needing the
		// database, not that it parses its input.
		{"import", []string{"import", snapshot}},
		{"serve", []string{"serve"}},
		{"migrate", []string{"migrate"}},
		{"doctor", []string{"doctor"}},
	}

	// The same guidance for every command, so an operator only has to learn
	// it once. config.Load prefixes its errors with "config:", which is why
	// the shared part is matched rather than the whole message.
	const guidance = "DATABASE_URL or DATABASE_URL_FILE is required"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := runWithoutDatabase(t, tt.args...)

			require.Error(t, run.err, "%s with no DATABASE_URL must fail", tt.name)
			assert.Contains(t, run.err.Error(), "DATABASE_URL",
				"the message must name the variable the operator has to set")
			assert.Contains(t, run.err.Error(), guidance)

			assert.Zero(t, run.rpc.accepted.Load(),
				"%s must fail on configuration rather than connect somewhere", tt.name)

			// A connection attempt would not come back before a dial
			// timeout (RPC's is 30s); a configuration error is immediate.
			assert.Less(t, run.elapsed, 2*time.Second,
				"%s must fail fast rather than dial", tt.name)
		})
	}
}

// TestHelpAndVersionNeedNoConfiguration pins the exceptions: a deployment can
// ask what the binary is and what it does before it is configured. "version"
// is the --version flag — there is no version subcommand.
func TestHelpAndVersionNeedNoConfiguration(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"--help", []string{"--help"}},
		{"--version", []string{"--version"}},
		{"codegen --help", []string{"codegen", "--help"}},
		{"help codegen", []string{"help", "codegen"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := runWithoutDatabase(t, tt.args...)

			require.NoError(t, run.err, "%s needs no configuration", tt.name)
			assert.NotEmpty(t, run.output, "%s must still print something", tt.name)
			assert.Zero(t, run.rpc.accepted.Load())
		})
	}
}

// TestRequireDatabaseURL covers the helper migrate calls before it connects:
// the file-style secret is an equal way to provide the connection string, and
// providing both is a mistake rather than a preference.
func TestRequireDatabaseURL(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		t.Setenv("DATABASE_URL_FILE", "")

		_, err := requireDatabaseURL()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DATABASE_URL")
	})

	t.Run("from the environment", func(t *testing.T) {
		want := testDatabaseURL
		t.Setenv("DATABASE_URL", want)
		t.Setenv("DATABASE_URL_FILE", "")

		got, err := requireDatabaseURL()
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("from a file", func(t *testing.T) {
		want := testDatabaseURL
		path := filepath.Join(t.TempDir(), "database-url")
		// Secret files usually end with a newline; the driver must not be
		// handed one.
		require.NoError(t, os.WriteFile(path, []byte(want+"\n"), 0o600))

		t.Setenv("DATABASE_URL", "")
		t.Setenv("DATABASE_URL_FILE", path)

		got, err := requireDatabaseURL()
		require.NoError(t, err)
		assert.Equal(t, want, got, "the trailing newline is trimmed")
	})

	t.Run("both", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://localhost/sorovault")
		t.Setenv("DATABASE_URL_FILE", filepath.Join(t.TempDir(), "database-url"))

		_, err := requireDatabaseURL()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not both")
	})
}

// TestMigrateWithoutADatabaseNamesTheVariable covers the one command that
// checks for the database itself instead of going through config.Load: its
// message has no "config:" prefix, but it names the same variables.
func TestMigrateWithoutADatabaseNamesTheVariable(t *testing.T) {
	run := runWithoutDatabase(t, "migrate")

	require.Error(t, run.err)
	assert.Equal(t, "DATABASE_URL or DATABASE_URL_FILE is required", run.err.Error(),
		"migrate reports the requirement exactly, with no prefix")
	assert.Zero(t, run.rpc.accepted.Load())
}

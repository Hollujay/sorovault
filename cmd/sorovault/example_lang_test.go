package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodegenAcceptsTheExamplesLanguage checks `--lang examples` is a first
// class value alongside ts and md: accepted ones get as far as needing the
// database, and rejected ones fail with the message that names the choice.
func TestCodegenAcceptsTheExamplesLanguage(t *testing.T) {
	// No t.Parallel: runWithoutDatabase changes the process environment.

	t.Run("examples is a supported language", func(t *testing.T) {
		run := runWithoutDatabase(t, "codegen", testContractID, "--lang", "examples")
		// Accepted, so the command runs past validation and stops where
		// every other command stops with no database at all.
		require.Error(t, run.err)
		assert.Contains(t, run.err.Error(), "DATABASE_URL")
		assert.Zero(t, run.rpc.accepted.Load(),
			"validation passing sends the command to the database, not to RPC")
	})

	t.Run("an unknown language is refused", func(t *testing.T) {
		run := runWithoutDatabase(t, "codegen", testContractID, "--lang", "yaml")
		require.Error(t, run.err)
		assert.Contains(t, run.err.Error(), "unsupported --lang")
		assert.Contains(t, run.err.Error(), "examples", "the message names what is supported")
		assert.Zero(t, run.rpc.accepted.Load(),
			"an unsupported language is refused before anything is contacted")
	})

	t.Run("examples cannot be packaged", func(t *testing.T) {
		run := runWithoutDatabase(t, "codegen", testContractID,
			"--lang", "examples", "--package", "--name", "@acme/example")
		require.Error(t, run.err)
		assert.Contains(t, run.err.Error(), "--lang examples cannot be packaged")
		assert.Zero(t, run.rpc.accepted.Load())
	})

	t.Run("md is still refused as a package", func(t *testing.T) {
		run := runWithoutDatabase(t, "codegen", testContractID, "--lang", "md", "--package", "--name", "@acme/example")
		require.Error(t, run.err)
		assert.Contains(t, run.err.Error(), "--lang md cannot be packaged")
	})
}

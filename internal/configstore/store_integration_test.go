//go:build integration && !windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestConcurrentCommitSubprocesses(t *testing.T) {
	if os.Getenv("CONFIGSTORE_COMMIT_HELPER") == "1" {
		runCommitSubprocessHelper(t)
		return
	}

	root := privateStoreDir(t)
	configPath := filepath.Join(root, "config.toml")
	source := validStoreConfig(t)
	writePrivateStoreFile(t, configPath, source, 0o600)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable: %v", err)
	}
	type child struct {
		command *exec.Cmd
		output  bytes.Buffer
		ready   string
		result  string
	}
	gate := filepath.Join(root, "commit.gate")
	children := make([]child, 2)
	for index, model := range []string{"gpt-subprocess-one", "gpt-subprocess-two"} {
		ready := filepath.Join(root, model+".ready")
		result := filepath.Join(root, model+".result")
		command := exec.Command(executable, "-test.run=^TestConcurrentCommitSubprocesses$") // #nosec G204 -- exact current test binary.
		command.Env = append(os.Environ(),
			"CONFIGSTORE_COMMIT_HELPER=1",
			"CONFIGSTORE_CONFIG_PATH="+configPath,
			"CONFIGSTORE_READY_PATH="+ready,
			"CONFIGSTORE_RESULT_PATH="+result,
			"CONFIGSTORE_GATE_PATH="+gate,
			"CONFIGSTORE_MODEL="+model,
		)
		children[index] = child{command: command, ready: ready, result: result}
		children[index].command.Stdout = &children[index].output
		children[index].command.Stderr = &children[index].output
		if err := children[index].command.Start(); err != nil {
			t.Fatalf("start child %d: %v", index, err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		allReady := true
		for index := range children {
			if _, err := os.Lstat(children[index].ready); err != nil {
				allReady = false
			}
		}
		if allReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("subprocesses did not load the shared base in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatalf("publish subprocess gate: %v", err)
	}
	for index := range children {
		if err := children[index].command.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", index, err, children[index].output.String())
		}
	}
	committed := 0
	lost := 0
	for index := range children {
		value, err := os.ReadFile(children[index].result) // #nosec G304 -- exact test-owned path.
		if err != nil {
			t.Fatalf("read child %d result: %v", index, err)
		}
		switch string(value) {
		case "committed\n":
			committed++
		case "lost\n":
			lost++
		default:
			t.Fatalf("child %d result = %q", index, value)
		}
	}
	if committed != 1 || lost != 1 {
		t.Fatalf("subprocess outcomes: committed=%d lost=%d", committed, lost)
	}
	assertPrivateFileBytes(t, BackupPath(configPath), source)
	assertPersistentLockAndNoTransactionTemps(t, configPath, "")
}

func runCommitSubprocessHelper(t *testing.T) {
	configPath := os.Getenv("CONFIGSTORE_CONFIG_PATH")
	readyPath := os.Getenv("CONFIGSTORE_READY_PATH")
	resultPath := os.Getenv("CONFIGSTORE_RESULT_PATH")
	gatePath := os.Getenv("CONFIGSTORE_GATE_PATH")
	model := os.Getenv("CONFIGSTORE_MODEL")
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("helper Load: %v", err)
	}
	if err := os.WriteFile(readyPath, nil, 0o600); err != nil {
		t.Fatalf("helper ready: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(gatePath); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("helper gate: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("helper gate timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	candidate := bytes.Replace(base.Bytes(), []byte("gpt-test"), []byte(model), 1)
	result, commitErr := NewWriter().Commit(context.Background(), Mutation{
		Base: base, Candidate: candidate,
	}, nil)
	outcome := ""
	switch {
	case result.State == CommitCommitted && commitErr == nil:
		outcome = "committed\n"
	case result == (CommitResult{}) && errors.Is(commitErr, ErrUnsafePath):
		outcome = "lost\n"
	default:
		t.Fatalf("helper Commit = %#v, %v", result, commitErr)
	}
	if err := os.WriteFile(resultPath, []byte(outcome), 0o600); err != nil {
		t.Fatalf("helper result: %v", err)
	}
}

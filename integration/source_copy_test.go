// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package integration_test

import (
	"context"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This test-only process builds a copied core in a new host module. The runtime
// itself never imports os/exec and does not expose a shell or command tool.
func TestCoreCanBeSourceEmbeddedOffline(t *testing.T) {
	destination := t.TempDir()
	const original = "github.com/hwj123hwj/easyagent-server-core"
	const host = "example.test/embedded-host"
	err := filepath.WalkDir("../core", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, data, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range parsed.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(name, ".") && !strings.HasPrefix(name, original+"/core/") {
				t.Fatalf("non-core dependency %s", name)
			}
			if name == "os/exec" || name == "syscall" || name == "plugin" || name == "net/http" {
				t.Fatalf("unexpected runtime capability %s", name)
			}
		}
		relative, err := filepath.Rel("..", path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, []byte(strings.ReplaceAll(string(data), original, host)), 0644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "go.mod"), []byte("module "+host+"\n\ngo 1.24.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	program := `package main
import (
 "context"
 "encoding/json"
 "fmt"
 "time"
 "example.test/embedded-host/core/model"
 "example.test/embedded-host/core/runner"
 "example.test/embedded-host/core/tool"
)
func main() {
 p := model.ProviderFunc(func(ctx context.Context,r model.Request,emit func(string)error)(model.Response,error){
  if len(r.Messages)==1{return model.Response{FinishReason:model.ToolUse,ToolCalls:[]model.ToolCall{{ID:"c1",Name:"inspect",Arguments:json.RawMessage("{}")}}},nil}
  return model.Response{FinishReason:model.Stop,Text:"embedded core works"},nil
 })
 t := tool.Tool{Spec:tool.Spec{Name:"inspect",Version:"1",Effect:tool.Read,Input:tool.Schema{Type:"object"},Timeout:time.Second,MaxResultBytes:1024},Execute:func(context.Context,tool.ExecutionContext,tool.Call,tool.Progress)(tool.Result,error){return tool.Result{Status:tool.Available,Summary:"fixture"},nil}}
 r,err:=runner.New(runner.Definition{ID:"host",Version:"1",Model:"fixture",Tools:[]tool.Tool{t}},runner.Dependencies{Provider:p,Authorizer:tool.AuthorizerFunc(func(context.Context,tool.ExecutionContext,tool.Call)error{return nil})});if err!=nil{panic(err)}
 out,err:=r.Run(context.Background(),runner.Request{RunID:"run",Principal:tool.Principal{TenantID:"host",SubjectID:"user"},Question:"inspect"},nil);if err!=nil{panic(err)};fmt.Println(out.Text)
}
`
	if err := os.WriteFile(filepath.Join(destination, "main.go"), []byte(program), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", ".")
	cmd.Dir = destination
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GOWORK=") && !strings.HasPrefix(env, "GOPROXY=") && !strings.HasPrefix(env, "GOSUMDB=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("copied core build failed: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) != "embedded core works" {
		t.Fatalf("unexpected output %q", output)
	}
}

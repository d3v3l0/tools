// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fake

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/internal/gocommand"
	"golang.org/x/tools/internal/lsp/protocol"
	"golang.org/x/tools/txtar"
)

// Sandbox holds a collection of temporary resources to use for working with Go
// code in tests.
type Sandbox struct {
	name    string
	gopath  string
	basedir string
	env     []string
	Proxy   *Proxy
	Workdir *Workdir
}

// NewSandbox creates a collection of named temporary resources, with a
// working directory populated by the txtar-encoded content in srctxt, and a
// file-based module proxy populated with the txtar-encoded content in
// proxytxt.
func NewSandbox(name, srctxt, proxytxt string, env ...string) (_ *Sandbox, err error) {
	sb := &Sandbox{
		name: name,
		env:  env,
	}
	defer func() {
		// Clean up if we fail at any point in this constructor.
		if err != nil {
			sb.Close()
		}
	}()
	basedir, err := ioutil.TempDir("", fmt.Sprintf("goplstest-sandbox-%s-", name))
	if err != nil {
		return nil, fmt.Errorf("creating temporary workdir: %v", err)
	}
	sb.basedir = basedir
	sb.gopath = filepath.Join(sb.basedir, "gopath")
	workdir := filepath.Join(sb.basedir, "work")
	proxydir := filepath.Join(sb.basedir, "proxy")
	for _, subdir := range []string{sb.gopath, workdir, proxydir} {
		if err := os.Mkdir(subdir, 0755); err != nil {
			return nil, err
		}
	}
	sb.Proxy, err = NewProxy(proxydir, proxytxt)
	sb.Workdir, err = NewWorkdir(workdir, srctxt)
	return sb, nil
}

func unpackTxt(txt string) map[string][]byte {
	dataMap := make(map[string][]byte)
	archive := txtar.Parse([]byte(txt))
	for _, f := range archive.Files {
		dataMap[f.Name] = f.Data
	}
	return dataMap
}

// splitModuleVersionPath extracts module information from files stored in the
// directory structure modulePath@version/suffix.
// For example:
//  splitModuleVersionPath("mod.com@v1.2.3/package") = ("mod.com", "v1.2.3", "package")
func splitModuleVersionPath(path string) (modulePath, version, suffix string) {
	parts := strings.Split(path, "/")
	var modulePathParts []string
	for i, p := range parts {
		if strings.Contains(p, "@") {
			mv := strings.SplitN(p, "@", 2)
			modulePathParts = append(modulePathParts, mv[0])
			return strings.Join(modulePathParts, "/"), mv[1], strings.Join(parts[i+1:], "/")
		}
		modulePathParts = append(modulePathParts, p)
	}
	// Default behavior: this is just a module path.
	return path, "", ""
}

// GOPATH returns the value of the Sandbox GOPATH.
func (sb *Sandbox) GOPATH() string {
	return sb.gopath
}

// GoEnv returns the default environment variables that can be used for
// invoking Go commands in the sandbox.
func (sb *Sandbox) GoEnv() []string {
	return append([]string{
		"GOPATH=" + sb.GOPATH(),
		"GOPROXY=" + sb.Proxy.GOPROXY(),
		"GO111MODULE=",
		"GOSUMDB=off",
	}, sb.env...)
}

// RunGoCommand executes a go command in the sandbox.
func (sb *Sandbox) RunGoCommand(ctx context.Context, verb string, args ...string) error {
	inv := gocommand.Invocation{
		Verb:       verb,
		Args:       args,
		WorkingDir: sb.Workdir.workdir,
		Env:        sb.GoEnv(),
	}
	gocmdRunner := &gocommand.Runner{}
	_, stderr, _, err := gocmdRunner.RunRaw(ctx, inv)
	if err != nil {
		return err
	}
	// Hardcoded "file watcher": If the command executed was "go mod init",
	// send a file creation event for a go.mod in the working directory.
	if strings.HasPrefix(stderr.String(), "go: creating new go.mod") {
		modpath := filepath.Join(sb.Workdir.workdir, "go.mod")
		sb.Workdir.sendEvents(ctx, []FileEvent{{
			Path: modpath,
			ProtocolEvent: protocol.FileEvent{
				URI:  toURI(modpath),
				Type: protocol.Created,
			},
		}})
	}
	return nil
}

// Close removes all state associated with the sandbox.
func (sb *Sandbox) Close() error {
	var goCleanErr error
	if sb.gopath != "" {
		if err := sb.RunGoCommand(context.Background(), "clean", "-modcache"); err != nil {
			goCleanErr = fmt.Errorf("cleaning modcache: %v", err)
		}
	}
	err := os.RemoveAll(sb.basedir)
	if err != nil || goCleanErr != nil {
		return fmt.Errorf("error(s) cleaning sandbox: cleaning modcache: %v; removing files: %v", goCleanErr, err)
	}
	return nil
}

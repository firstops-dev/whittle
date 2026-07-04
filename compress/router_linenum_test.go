package compress

import (
	"fmt"
	"strings"
	"testing"
)

// numbered renders content as a Read-tool file read: "N\t<line>".
func numbered(body string) string {
	lines := strings.Split(body, "\n")
	var b strings.Builder
	for i, l := range lines {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, l)
	}
	return b.String()
}

// TestLineNumbered_NeverFiresOnCode: a line-numbered read of REAL code in any
// common language must NEVER route to doc_read (the lossy prose model). Strictly
// markdown-only. Every snippet here is representative source, including the
// sneaky ones whose comment syntax (`# ...`) looks like markdown headings.
func TestLineNumbered_NeverFiresOnCode(t *testing.T) {
	cases := map[string]string{
		"python": `#!/usr/bin/env python3
# Module entrypoint
# Handles request routing
import os
import sys

def main(argv):
    cfg = load_config(argv[1])
    for name in cfg.handlers:
        register(name)
    return 0

class Handler:
    def __init__(self, name):
        self.name = name

if __name__ == "__main__":
    sys.exit(main(sys.argv))`,
		"java": `// Service entrypoint
package com.example.app;

import java.util.List;

public class Server {
    private final int port;

    public Server(int port) {
        this.port = port;
    }

    public void start() {
        List<String> routes = loadRoutes();
        routes.forEach(this::register);
    }
}`,
		"javascript": `// api client
const axios = require('axios');

async function fetchUsers(page) {
  const res = await axios.get('/api/users', { params: { page } });
  return res.data.items;
}

module.exports = { fetchUsers };`,
		"typescript": `import { Request, Response } from 'express';

interface User {
  id: number;
  name: string;
}

export function handler(req: Request, res: Response): void {
  const users: User[] = loadUsers();
  res.json(users);
}`,
		"go": `package main

import "fmt"

func main() {
	for i := 0; i < 10; i++ {
		fmt.Println(process(i))
	}
}

func process(n int) int {
	return n * n
}`,
		"c": `#include <stdio.h>
#include <stdlib.h>

static int counter = 0;

int main(int argc, char **argv) {
    for (int i = 0; i < argc; i++) {
        printf("%s\n", argv[i]);
    }
    return EXIT_SUCCESS;
}`,
		"cpp": `#include <vector>
#include <string>

namespace app {

class Registry {
 public:
    void add(const std::string& name) {
        names_.push_back(name);
    }
 private:
    std::vector<std::string> names_;
};

}  // namespace app`,
		"rust": `use std::collections::HashMap;

pub struct Cache {
    entries: HashMap<String, String>,
}

impl Cache {
    pub fn new() -> Self {
        Cache { entries: HashMap::new() }
    }
}`,
		"ruby": `# frozen_string_literal: true
# Rack application config

require 'sinatra'

class App < Sinatra::Base
  get '/health' do
    json status: 'ok'
  end
end`,
		"shell": `#!/bin/bash
# Deploy script
# Usage: deploy.sh <env>
set -euo pipefail

ENV="$1"
BUILD_DIR="/tmp/build"

echo "deploying to $ENV"
rsync -az "$BUILD_DIR/" "web@$ENV:/srv/app/"
ssh "web@$ENV" systemctl restart app`,
		"sql": `-- user reporting queries
SELECT u.id, u.name, COUNT(o.id) AS orders
FROM users u
LEFT JOIN orders o ON o.user_id = u.id
WHERE u.created_at > '2026-01-01'
GROUP BY u.id, u.name
HAVING COUNT(o.id) > 5
ORDER BY orders DESC;`,
		"yaml": `# service deployment config
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 3
  template:
    spec:
      containers:
        - name: web
          image: registry/web:1.2.3
          ports:
            - containerPort: 8080`,
		"css": `/* layout styles */
.container {
  display: flex;
  gap: 1rem;
}

.card > h2 {
  font-size: 1.25rem;
  color: #10b981;
}`,
		"html": `<!doctype html>
<html>
<head><title>App</title></head>
<body>
  <div class="container">
    <p>Hello</p>
  </div>
</body>
</html>`,
		"makefile": `# build targets
.PHONY: build test

build:
	go build -o bin/app ./cmd/app

test:
	go test ./...

clean:
	rm -rf bin`,
		// Regression: a real-corpus comment-rich Makefile leaked past the
		// heading+sentence checks (its comments mimic markdown headings AND read
		// as sentences; `:=` lines dodge the code regexes). assignLineRe rejects it.
		"makefile_comment_rich": `# Build configuration for the native runtime.
# This file drives every developer and CI build of the project.
CXX       := clang++
CXXFLAGS  := -std=c++23 -Wall -Wextra -Wpedantic -Iinclude
LDFLAGS   :=

# The default target builds the optimized binary for the host platform.
all: bin/app

# Tests compile with sanitizers enabled to catch memory errors early.
test: bin/test
	./bin/test`,
		"dockerfile": `# build stage
FROM golang:1.22 AS build
WORKDIR /src
COPY . .
RUN go build -o /out/app ./cmd/app

# runtime stage
FROM gcr.io/distroless/static
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]`,
		// Reviewer B1: prose-heavy YAML (doc comments + sentence-like values)
		// dodged the =-keyed assignLineRe and reached the paraphraser (an Ansible
		// playbook lost its become: yes). kvLineRe now rejects any `key: value`.
		"yaml_ansible_prosey": `# Playbook for provisioning the production web tier.
# This playbook configures nginx and hardens the firewall for every host.
- name: Ensure nginx is installed on every production host in the fleet.
  apt:
    name: nginx
    state: present
  become: yes

# The handler below restarts the service whenever the config template changes.
- name: Restart nginx when the configuration changes on disk.
  service:
    name: nginx
    state: restarted
  become: yes`,
		"compose_prosey": `# Compose file for the local development environment of the project.
# Each service below maps to one container in the developer stack setup.
services:
  web:
    image: registry/web:1.2.3
    ports:
      - "8080:8080"
  db:
    image: mysql:8
    environment:
      MYSQL_DATABASE: app`,
		// Reviewer O1: a heavily-commented Dockerfile whose comments read as
		// sentences; dockerDirectiveRe now rejects the directive lines.
		"dockerfile_prosey": `# This stage compiles the binary with the full toolchain available.
# The resulting artifact is copied into a minimal runtime image below.
FROM golang:1.22 AS build
WORKDIR /src
COPY . .
RUN go build -o /out/app ./cmd/app

# The runtime stage keeps the image small and the attack surface minimal.
FROM gcr.io/distroless/static
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]`,
		"toml": `# app configuration
[server]
host = "0.0.0.0"
port = 8080

[database]
url = "mysql://localhost:3306/app"
pool_size = 10`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			ct, _ := Detect(numbered(body))
			if ct == TypeDocRead {
				t.Fatalf("%s source routed to doc_read (would be paraphrased) — must stay code/skip", name)
			}
		})
	}
}

// TestLineNumbered_FiresOnlyOnRealMarkdown: the positive contract — genuine
// markdown docs route to doc_read; near-markdown (fence, one heading, embedded
// code) does not.
func TestLineNumbered_FiresOnlyOnRealMarkdown(t *testing.T) {
	md := `# Service Overview

This document describes the deployment topology for the ingestion service.

## Architecture

Events arrive at the edge, are validated against the schema registry, and are
written to the durable queue before workers pick them up for enrichment.

## Operations

On-call engineers should consult the runbook before restarting any worker.`
	if ct, _ := Detect(numbered(md)); ct != TypeDocRead {
		t.Fatalf("genuine markdown doc read detected as %q, want doc_read", ct)
	}
}

// Auditor regression: SMALL/PARTIAL code reads (snippet-shaped Python: dotted
// from-imports, bare assignments, try/except headers) were routed prose and
// lossily compressed — "from .packages import chardet" was deleted. Must be code.
func TestDetect_SmallPythonSnippetIsCode(t *testing.T) {
	snippet := `"""Module compatibility layer."""

import sys

from .packages import chardet

_ver = sys.version_info
is_py2 = _ver[0] == 2
is_py3 = _ver[0] == 3

try:
    import simplejson as json
except ImportError:
    import json

builtin_str = str
bytes = bytes
str = str
`
	if ct, _ := Detect(snippet); ct != TypeCode {
		t.Fatalf("small python snippet detected as %q, want code (would be paraphrased)", ct)
	}
}

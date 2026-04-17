# Zoekt AI Assistant Guidelines

## Project Overview
Zoekt is a fast text search engine intended for use with source code. It consists of a core search/indexing library, a web interface, and several tools to mirror and index repositories from various source code hosting platforms (GitHub, GitLab, Gerrit, Bitbucket).

## Tech Stack
* **Language:** Go (1.24+)
* **Key Libraries:** `github.com/go-git/go-git/v5`, `github.com/google/go-github`, `github.com/xanzy/go-gitlab`

## Build & Test Commands
* **Build:** `go build ./...`
* **Test:** `go test ./...`
* **Lint & Vet:** `go vet ./...` and format with `gofmt`

## Codebase Organization
* `cmd/`: Contains all the executable binaries (e.g., `zoekt`, `zoekt-indexserver`, `zoekt-webserver`, and mirroring tools).
* `build/`: Index building logic.
* `gitindex/`: Git repository indexing logic.
* `query/`: Query parsing and Abstract Syntax Tree (AST).
* `web/`: Web interface templates and server logic.
* `shards/`: Sharded index management.
* Root Directory: Core search and indexing data structures (`indexdata.go`, `matchtree.go`, etc.).

## Engineering Conventions
* **Standard Go Idioms:** Strictly adhere to standard Go formatting, explicit error handling, and idiomatic concurrency patterns.
* **Format Stability:** Zoekt indexes are persisted to disk. Any modifications to the index structure or serialization logic (`indexdata.go`, `write.go`, `read.go`) must be approached with extreme caution to maintain backward compatibility.
* **Performance:** Zoekt is optimized for speed. Minimize memory allocations in the hot paths (search and matching).
* **Testing:** Maintain comprehensive test coverage. *Note: When testing git submodule behavior locally, you must explicitly allow the file protocol (e.g., `git -c protocol.file.allow=always`) due to modern Git security defaults.*

## Git Commit Rules
* Use "conventional commits v1.0.0" for the commit title.
* Append the following note to the last line of the commit message (below any summaries):
  ```
  This commit has been created by an automated coding assistant,
  with human supervision.
  ```
* Additionally, append the exact prompt used to generate the commit in full.

# Example: Splitting a flat package into subpackages

This example walks through using `gorelo` to refactor a single `app` package
into focused subpackages — `server`, `db`, and `service` — while renaming
private identifiers to exported ones so they work across package boundaries.

## Before

A flat package `app/` with everything in one place:

```
app/
    server.go      — Server, serverOptions, handleRequest, ...
    db.go          — DB, dbOptions, runMigrations, ...
    service.go     — Service, serviceOptions, processJob, ...
    main.go        — wires everything together
```

```go
// app/server.go
package app

type serverOptions struct {
    addr    string
    maxConn int
}

type Server struct {
    opts serverOptions
    db   *DB
}

func newServer(opts serverOptions, db *DB) *Server {
    return &Server{opts: opts, db: db}
}

func (s *Server) handleRequest() error {
    return s.db.queryRow()
}
```

```go
// app/db.go
package app

type dbOptions struct {
    dsn     string
    maxIdle int
}

type DB struct {
    opts dbOptions
}

func newDB(opts dbOptions) *DB {
    return &DB{opts: opts}
}

func (d *DB) queryRow() error {
    return nil
}

func (d *DB) runMigrations() error {
    return nil
}
```

```go
// app/service.go
package app

type serviceOptions struct {
    workers int
    retries int
}

type Service struct {
    opts   serviceOptions
    server *Server
}

func newService(opts serviceOptions, server *Server) *Service {
    return &Service{opts: opts, server: server}
}

func (s *Service) processJob() error {
    return s.server.handleRequest()
}
```

## Rules file

Create `gorelo.rules`:

```
# Each group moves declarations to a new subpackage and renames
# private identifiers to exported ones so they're accessible
# across packages.

# --- server package ---

server/server.go <-
    Server
    serverOptions=Options
    newServer=New

# Server methods follow automatically.
# handleRequest is private and used only internally,
# but it calls db.queryRow across the new package boundary,
# so it needs to be exported too.

server/server.go <-
    Server#handleRequest=HandleRequest
    Server#opts=Opts

# --- db package ---

db/db.go <-
    DB
    dbOptions=Options
    newDB=New

db/db.go <-
    DB#queryRow=QueryRow
    DB#runMigrations=RunMigrations
    DB#opts=Opts

# --- service package ---

service/service.go <-
    Service
    serviceOptions=Options
    newService=New

service/service.go <-
    Service#processJob=ProcessJob
    Service#opts=Opts
    Service#server=Server
```

## Running

Preview the changes first:

```bash
gorelo check gorelo.rules
```

Then apply:

```bash
gorelo apply gorelo.rules
```

## After

```
app/
    server/
        server.go   — Server, Options, New, HandleRequest
    db/
        db.go       — DB, Options, New, QueryRow, RunMigrations
    service/
        service.go  — Service, Options, New, ProcessJob
    main.go         — updated imports and references
```

```go
// app/server/server.go
package server

import "app/db"

type Options struct {
    Addr    string
    MaxConn int
}

type Server struct {
    Opts *Options
    DB   *db.DB
}

func New(opts Options, d *db.DB) *Server {
    return &Server{Opts: opts, DB: d}
}

func (s *Server) HandleRequest() error {
    return s.DB.QueryRow()
}
```

```go
// app/db/db.go
package db

type Options struct {
    DSN     string
    MaxIdle int
}

type DB struct {
    Opts Options
}

func New(opts Options) *DB {
    return &DB{Opts: opts}
}

func (d *DB) QueryRow() error {
    return nil
}

func (d *DB) RunMigrations() error {
    return nil
}
```

```go
// app/service/service.go
package service

import "app/server"

type Options struct {
    Workers int
    Retries int
}

type Service struct {
    Opts   Options
    Server *server.Server
}

func New(opts Options, srv *server.Server) *Service {
    return &Service{Opts: opts, Server: srv}
}

func (s *Service) ProcessJob() error {
    return s.Server.HandleRequest()
}
```

Consumer code in `main.go` (and any other package that imports `app`) is
automatically updated to use the new package paths and exported names.

## Generating backward-compatibility stubs

If you want to keep the old `app.Server` etc. working during a transition
period, add `--stubs`:

```bash
gorelo apply --stubs -f gorelo.rules
```

This generates `//go:fix` type aliases and wrapper functions in the original
package so existing consumers continue to compile while you migrate them
incrementally.

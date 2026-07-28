package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/internal/lsn"
	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/destwire"
	"github.com/zourzouvillys/laredo/snapshotter/sourcesub"
	"github.com/zourzouvillys/laredo/source/pg"
)

// archiveCmd dispatches `laredo archive <subcommand>`. Unlike most commands it
// talks to object storage directly (a snapshotter archive), not a laredo-server,
// so it works offline — useful for forensics and onboard reconstruction.
func archiveCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo archive <export|reconstruct>")
		os.Exit(1)
	}
	switch args[0] {
	case "export":
		archiveExportCmd(args[1:])
	case "reconstruct":
		archiveReconstructCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown archive command: %s\n", args[0]) //nolint:gosec // CLI output
		os.Exit(1)
	}
}

// archiveExportCmd exports a table from a PostgreSQL source into a snapshotter
// archive on disk (EDR-0006) — an offline backup, or a seed a laredo-server
// `file` source (or `laredo archive reconstruct`) can later replay with no
// database. It connects directly to PostgreSQL, in keeping with the offline-first
// archive command family. With --follow it keeps the archive live (base snapshot
// plus periodic diffs) until interrupted; otherwise it writes a one-shot snapshot.
func archiveExportCmd(args []string) {
	fs := flag.NewFlagSet("archive export", flag.ExitOnError)
	connection := fs.String("connection", "", "PostgreSQL connection string (required)")
	schema := fs.String("schema", "public", "table schema")
	table := fs.String("table", "", "table name (required)")
	store := fs.String("store", "local", "archive store: local or s3")
	path := fs.String("path", "", "local store path (store=local)")
	bucket := fs.String("bucket", "", "s3 bucket (store=s3)")
	prefix := fs.String("prefix", "", "s3 object prefix (store=s3)")
	region := fs.String("region", "", "s3 region (store=s3)")
	keyPrefix := fs.String("key-prefix", "", "archive key prefix a reader must match (default: <schema>.<table>/)")
	format := fs.String("format", "jsonl", "artifact format: jsonl or protobuf")
	follow := fs.Bool("follow", false, "keep the archive live (base + periodic diffs) until interrupted")
	diffInterval := fs.Duration("diff-interval", 30*time.Second, "diff flush interval (--follow)")
	parseGlobalFlags(fs, args)

	if *connection == "" || *table == "" {
		fmt.Fprintln(os.Stderr, "usage: laredo archive export --connection <dsn> --schema <s> --table <t> --store <local|s3> [store flags] [--key-prefix <p>] [--follow]")
		os.Exit(1)
	}
	tbl := laredo.Table(*schema, *table)
	kp := *keyPrefix
	if kp == "" {
		kp = tbl.String() + "/"
	}
	o := exportOpts{store: *store, path: *path, bucket: *bucket, prefix: *prefix, region: *region, keyPrefix: kp, format: *format}
	src := pg.New(pg.Connection(*connection))

	if *follow {
		// The adapter/Writer own the source lifecycle (Writer.Run defers sub.Stop,
		// which closes the source), so do not also close it here.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		fmt.Fprintf(os.Stderr, "archiving %s to %s every %s; press Ctrl-C to stop\n", tbl, kp, *diffInterval) //nolint:gosec // CLI output
		if err := exportFollow(ctx, src, tbl, o, *diffInterval); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ctx := context.Background()
	defer func() { _ = src.Close(ctx) }()
	n, pos, err := exportArchive(ctx, src, tbl, o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	printJSON(map[string]any{
		"table":      tbl.String(),
		"position":   pos,
		"row_count":  n,
		"key_prefix": kp,
	})
}

// exportFollow continuously archives a source: it drives the snapshotter Writer
// through the sourcesub adapter, writing a base snapshot then periodic diffs (and
// recording the schema in the manifest) until ctx is cancelled. Any source works;
// the CLI wires PostgreSQL.
func exportFollow(ctx context.Context, src laredo.SyncSource, table laredo.TableIdentifier, o exportOpts, diffInterval time.Duration) error {
	dest, err := destwire.BuildDestination(ctx, destwire.DestinationSpec{
		Type: o.store, Path: o.path, Bucket: o.bucket, Prefix: o.prefix, Region: o.region,
	}, destwire.AmbientAWSConfig)
	if err != nil {
		return err
	}
	formats, err := destwire.BuildFormats([]string{o.format})
	if err != nil {
		return err
	}
	w, err := snapshotter.New(sourcesub.New(src, table, nil), snapshotter.Config{
		Table:           table.String(),
		KeyPrefix:       o.keyPrefix,
		Policy:          snapshotter.Policy{DiffInterval: diffInterval},
		SnapshotFormats: formats,
		DiffFormats:     formats,
		Destinations:    []snapshotter.Destination{dest},
	})
	if err != nil {
		return err
	}
	return w.Run(ctx)
}

type exportOpts struct {
	store, path, bucket, prefix, region string
	keyPrefix, format                   string
}

// exportArchive drives a source through Init + Baseline to capture a table's
// current rows and schema, then writes them as a one-shot base-snapshot archive
// via snapshotter/destwire (the same destination/format wiring laredo-server and
// the snapshotter use). It works with any laredo.SyncSource, so it is exercised
// with an in-memory source in tests and PostgreSQL from the CLI. Returns the row
// count and the source position the snapshot reflects.
func exportArchive(ctx context.Context, src laredo.SyncSource, table laredo.TableIdentifier, o exportOpts) (int, string, error) {
	schemas, err := src.Init(ctx, laredo.SourceConfig{Tables: []laredo.TableIdentifier{table}})
	if err != nil {
		return 0, "", fmt.Errorf("init source: %w", err)
	}
	var rows []laredo.Row
	pos, err := src.Baseline(ctx, []laredo.TableIdentifier{table}, func(_ laredo.TableIdentifier, r laredo.Row) {
		rows = append(rows, r)
	})
	if err != nil {
		return 0, "", fmt.Errorf("baseline: %w", err)
	}
	posStr := src.PositionToString(pos)

	dest, err := destwire.BuildDestination(ctx, destwire.DestinationSpec{
		Type: o.store, Path: o.path, Bucket: o.bucket, Prefix: o.prefix, Region: o.region,
	}, destwire.AmbientAWSConfig)
	if err != nil {
		return 0, "", err
	}
	formats, err := destwire.BuildFormats([]string{o.format})
	if err != nil {
		return 0, "", err
	}
	if err := snapshotter.WriteBaseSnapshot(ctx, []snapshotter.Destination{dest}, o.keyPrefix, formats, table.String(), posStr, rows, schemas[table], time.Now()); err != nil {
		return 0, "", err
	}
	return len(rows), posStr, nil
}

// archiveReconstructCmd materializes a table's full state as of a source
// position, read from the snapshotter's cold archive (EDR-0003). It builds a
// reader through the same snapshotter/destwire path laredo-server uses.
func archiveReconstructCmd(args []string) {
	fs := flag.NewFlagSet("archive reconstruct", flag.ExitOnError)
	store := fs.String("store", "local", "archive store: local or s3")
	path := fs.String("path", "", "local store path (store=local)")
	bucket := fs.String("bucket", "", "s3 bucket (store=s3)")
	prefix := fs.String("prefix", "", "s3 object prefix (store=s3)")
	region := fs.String("region", "", "s3 region (store=s3)")
	keyPrefix := fs.String("key-prefix", "", "archive key prefix (must match the snapshotter)")
	format := fs.String("format", "jsonl", "artifact format: jsonl or protobuf")
	keyFields := fs.String("key-fields", "", "comma-separated primary key columns (default: id)")
	at := fs.String("at", "", "source position (WAL LSN) to reconstruct as of (required)")
	parseGlobalFlags(fs, args)

	if *at == "" {
		fmt.Fprintln(os.Stderr, "usage: laredo archive reconstruct --at <position> --store <local|s3> [store flags] --key-prefix <p>")
		os.Exit(1)
	}

	var keys []string
	if *keyFields != "" {
		keys = strings.Split(*keyFields, ",")
	}

	rec, err := reconstructArchive(reconstructOpts{
		store: *store, path: *path, bucket: *bucket, prefix: *prefix, region: *region,
		keyPrefix: *keyPrefix, format: *format, keyFields: keys, at: *at,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if rec == nil {
		fmt.Fprintf(os.Stderr, "archive cannot reach position %q (empty archive, or its oldest snapshot is already after it)\n", *at) //nolint:gosec // CLI output
		os.Exit(1)
	}

	printJSON(map[string]any{
		"position":  rec.Position,
		"row_count": len(rec.Rows),
		"rows":      rec.Rows,
	})
}

type reconstructOpts struct {
	store, path, bucket, prefix, region string
	keyPrefix, format, at               string
	keyFields                           []string
}

// reconstructArchive builds a reader through snapshotter/destwire (the same path
// laredo-server uses) and materializes the table as of opts.at. It returns
// (nil, nil) when the archive cannot reach the position. No RPC deadline applies
// — it reads object storage and may fold many diffs; interrupt with Ctrl-C.
func reconstructArchive(o reconstructOpts) (*snapshotter.Reconstruction, error) {
	dest, err := destwire.BuildDestination(context.Background(), destwire.DestinationSpec{
		Type: o.store, Path: o.path, Bucket: o.bucket, Prefix: o.prefix, Region: o.region,
	}, destwire.AmbientAWSConfig)
	if err != nil {
		return nil, err
	}
	formats, err := destwire.BuildFormats([]string{o.format})
	if err != nil {
		return nil, err
	}
	reader, err := snapshotter.NewReader(dest, o.keyPrefix, formats...)
	if err != nil {
		return nil, err
	}
	return reader.ReconstructAsOf(context.Background(), o.at, o.keyFields, lsn.Compare)
}

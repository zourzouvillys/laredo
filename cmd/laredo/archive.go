package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/zourzouvillys/laredo/snapshotter"
	"github.com/zourzouvillys/laredo/snapshotter/destwire"
)

// archiveCmd dispatches `laredo archive <subcommand>`. Unlike most commands it
// talks to object storage directly (a snapshotter archive), not a laredo-server,
// so it works offline — useful for forensics and onboard reconstruction.
func archiveCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo archive <reconstruct>")
		os.Exit(1)
	}
	switch args[0] {
	case "reconstruct":
		archiveReconstructCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown archive command: %s\n", args[0]) //nolint:gosec // CLI output
		os.Exit(1)
	}
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
	return reader.ReconstructAsOf(context.Background(), o.at, o.keyFields, lsnCompare)
}

// lsnCompare orders two PostgreSQL WAL-LSN strings ("X/XXXXXXXX"). The empty or
// unparseable value sorts lowest. Archives written by a non-PostgreSQL source
// would need a different comparator; LSN is the overwhelming common case.
func lsnCompare(a, b string) int {
	la, aok := parseLSN(a)
	lb, bok := parseLSN(b)
	switch {
	case !aok && !bok:
		return 0
	case !aok:
		return -1
	case !bok:
		return 1
	case la < lb:
		return -1
	case la > lb:
		return 1
	default:
		return 0
	}
}

func parseLSN(s string) (uint64, bool) {
	hi, lo, ok := strings.Cut(s, "/")
	if !ok || hi == "" || lo == "" {
		return 0, false
	}
	high, err := strconv.ParseUint(hi, 16, 32)
	if err != nil {
		return 0, false
	}
	low, err := strconv.ParseUint(lo, 16, 32)
	if err != nil {
		return 0, false
	}
	return high<<32 | low, true
}

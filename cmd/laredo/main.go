// Command laredo is the CLI tool for interacting with a laredo-server instance.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/zourzouvillys/laredo"
	v1 "github.com/zourzouvillys/laredo/gen/laredo/v1"
	"github.com/zourzouvillys/laredo/gen/laredo/v1/laredov1connect"
)

var (
	address = "localhost:4001"
	timeout = 10 * time.Second
	output  = "table"
)

func main() {
	// Global flags parsed from env before subcommand.
	if v := os.Getenv("LAREDO_ADDRESS"); v != "" {
		address = v
	}

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "version":
		fmt.Printf("laredo %s\n", laredo.Version)
	case "ready":
		readyCmd(args)
	case "snapshot":
		snapshotCmd(args)
	case "query":
		queryCmd(args)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `laredo %s — CLI for laredo-server

Usage: laredo <command> [flags]

Commands:
  ready              Check if the server is ready
  snapshot list      List snapshots
  snapshot create    Create a new snapshot
  snapshot delete    Delete a snapshot
  snapshot inspect   Inspect a snapshot
  query count        Count rows in a table
  query get          Get a row by primary key
  version            Print version

Global settings:
  LAREDO_ADDRESS     Server address (default: localhost:4001)

`, laredo.Version)
}

func parseGlobalFlags(fs *flag.FlagSet, args []string) {
	fs.StringVar(&address, "address", address, "server address")
	fs.StringVar(&output, "output", output, "output format (table, json)")
	fs.DurationVar(&timeout, "timeout", timeout, "request timeout")
	fs.Parse(args) //nolint:errcheck // flags handle errors via ExitOnError
}

func oamClient() laredov1connect.LaredoOAMServiceClient {
	return laredov1connect.NewLaredoOAMServiceClient(
		http.DefaultClient,
		"http://"+address,
	)
}

func queryClient() laredov1connect.LaredoQueryServiceClient {
	return laredov1connect.NewLaredoQueryServiceClient(
		http.DefaultClient,
		"http://"+address,
	)
}

func ctx() context.Context {
	c, cancel := context.WithTimeout(context.Background(), timeout) //nolint:gosec // cancel deferred; short-lived CLI process
	defer cancel()
	return c
}

// --- ready ---

func readyCmd(args []string) {
	fs := flag.NewFlagSet("ready", flag.ExitOnError)
	source := fs.String("source", "", "check specific source readiness")
	parseGlobalFlags(fs, args)

	resp, err := oamClient().CheckReady(ctx(), connect.NewRequest(&v1.CheckReadyRequest{
		Source: *source,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if resp.Msg.GetReady() {
		fmt.Println("ready")
	} else {
		fmt.Println("not ready")
		for _, r := range resp.Msg.GetNotReadyReasons() {
			fmt.Printf("  - %s\n", r)
		}
		os.Exit(1)
	}
}

// --- snapshot ---

func snapshotCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo snapshot <list|create|delete|inspect>")
		os.Exit(1)
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "list":
		snapshotListCmd(subArgs)
	case "create":
		snapshotCreateCmd(subArgs)
	case "delete":
		snapshotDeleteCmd(subArgs)
	case "inspect":
		snapshotInspectCmd(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "unknown snapshot command: %s\n", sub)
		os.Exit(1)
	}
}

func snapshotListCmd(args []string) {
	fs := flag.NewFlagSet("snapshot list", flag.ExitOnError)
	table := fs.String("table", "", "filter by table (schema.table)")
	parseGlobalFlags(fs, args)

	resp, err := oamClient().ListSnapshots(ctx(), connect.NewRequest(&v1.ListSnapshotsRequest{
		Table: *table,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	snaps := resp.Msg.GetSnapshots()
	if output == "json" {
		printJSON(snaps)
		return
	}

	if len(snaps) == 0 {
		fmt.Println("no snapshots")
		return
	}

	fmt.Printf("%-40s  %-20s  %-10s  %s\n", "ID", "CREATED", "FORMAT", "TABLES")
	for _, s := range snaps {
		created := ""
		if s.GetCreatedAt() != nil {
			created = s.GetCreatedAt().AsTime().Format(time.DateTime)
		}
		tables := make([]string, 0, len(s.GetTables()))
		for _, t := range s.GetTables() {
			tables = append(tables, t.GetSchema()+"."+t.GetTable())
		}
		fmt.Printf("%-40s  %-20s  %-10s  %s\n", s.GetSnapshotId(), created, s.GetFormat(), strings.Join(tables, ", "))
	}
}

func snapshotCreateCmd(args []string) {
	fs := flag.NewFlagSet("snapshot create", flag.ExitOnError)
	parseGlobalFlags(fs, args)

	resp, err := oamClient().CreateSnapshot(ctx(), connect.NewRequest(&v1.CreateSnapshotRequest{}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if resp.Msg.GetAccepted() {
		fmt.Printf("snapshot created: %s\n", resp.Msg.GetMessage())
	} else {
		fmt.Fprintf(os.Stderr, "snapshot creation failed: %s\n", resp.Msg.GetMessage())
		os.Exit(1)
	}
}

func snapshotDeleteCmd(args []string) {
	fs := flag.NewFlagSet("snapshot delete", flag.ExitOnError)
	parseGlobalFlags(fs, args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo snapshot delete <snapshot-id>")
		os.Exit(1)
	}

	resp, err := oamClient().DeleteSnapshot(ctx(), connect.NewRequest(&v1.DeleteSnapshotRequest{
		SnapshotId: remaining[0],
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if resp.Msg.GetDeleted() {
		fmt.Println("snapshot deleted")
	}
}

func snapshotInspectCmd(args []string) {
	fs := flag.NewFlagSet("snapshot inspect", flag.ExitOnError)
	parseGlobalFlags(fs, args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo snapshot inspect <snapshot-id>")
		os.Exit(1)
	}

	resp, err := oamClient().InspectSnapshot(ctx(), connect.NewRequest(&v1.InspectSnapshotRequest{
		SnapshotId: remaining[0],
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	printJSON(resp.Msg.GetInfo())
}

// --- query ---

func queryCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo query <count|get>")
		os.Exit(1)
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "count":
		queryCountCmd(subArgs)
	case "get":
		queryGetCmd(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "unknown query command: %s\n", sub)
		os.Exit(1)
	}
}

func queryCountCmd(args []string) {
	fs := flag.NewFlagSet("query count", flag.ExitOnError)
	parseGlobalFlags(fs, args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo query count <schema.table>")
		os.Exit(1)
	}

	schema, table, ok := strings.Cut(remaining[0], ".")
	if !ok {
		fmt.Fprintln(os.Stderr, "table must be in schema.table format")
		os.Exit(1)
	}

	resp, err := queryClient().CountRows(ctx(), connect.NewRequest(&v1.CountRowsRequest{
		Schema: schema,
		Table:  table,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(resp.Msg.GetCount())
}

func queryGetCmd(args []string) {
	fs := flag.NewFlagSet("query get", flag.ExitOnError)
	pk := fs.Int64("pk", 0, "primary key value")
	parseGlobalFlags(fs, args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo query get --pk <id> <schema.table>")
		os.Exit(1)
	}

	schema, table, ok := strings.Cut(remaining[0], ".")
	if !ok {
		fmt.Fprintln(os.Stderr, "table must be in schema.table format")
		os.Exit(1)
	}

	resp, err := queryClient().GetRow(ctx(), connect.NewRequest(&v1.GetRowRequest{
		Schema:     schema,
		Table:      table,
		PrimaryKey: *pk,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if !resp.Msg.GetFound() {
		fmt.Fprintln(os.Stderr, "not found")
		os.Exit(1)
	}

	printJSON(resp.Msg.GetRow().AsMap())
}

func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

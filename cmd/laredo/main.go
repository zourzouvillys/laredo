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

const outputJSON = "json"

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
	case "status":
		statusCmd(args)
	case "ready":
		readyCmd(args)
	case "snapshot":
		snapshotCmd(args)
	case "query":
		queryCmd(args)
	case "reload":
		reloadCmd(args)
	case "pause":
		pauseCmd(args)
	case "resume":
		resumeCmd(args)
	case "dead-letters":
		deadLettersCmd(args)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd) //nolint:gosec // CLI output to stderr
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `laredo %s — CLI for laredo-server

Usage: laredo <command> [flags]

Commands:
  status             Show service status (pipelines, sources)
  ready              Check if the server is ready (exit 0/1)
  reload             Trigger re-baseline for a table
  pause              Pause a source
  resume             Resume a paused source
  snapshot list      List snapshots
  snapshot create    Create a new snapshot
  snapshot delete    Delete a snapshot
  snapshot inspect   Inspect a snapshot
  snapshot prune     Prune old snapshots
  query count        Count rows in a table
  query get          Get a row by primary key
  dead-letters       List dead letters for a pipeline
  dead-letters purge Purge dead letters
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

// --- status ---

func statusCmd(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	table := fs.String("table", "", "filter by table (schema.table)")
	parseGlobalFlags(fs, args)

	if *table != "" {
		// Table-specific status.
		schema, tbl, ok := strings.Cut(*table, ".")
		if !ok {
			fmt.Fprintln(os.Stderr, "table must be in schema.table format")
			os.Exit(1)
		}
		resp, err := oamClient().GetTableStatus(ctx(), connect.NewRequest(&v1.GetTableStatusRequest{
			Schema: schema,
			Table:  tbl,
		}))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if output == outputJSON {
			printJSON(resp.Msg)
			return
		}
		printPipelineTable(resp.Msg.GetPipelines())
		return
	}

	// Global status.
	resp, err := oamClient().GetStatus(ctx(), connect.NewRequest(&v1.GetStatusRequest{}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if output == outputJSON {
		printJSON(resp.Msg)
		return
	}

	fmt.Printf("Service State: %s\n\n", resp.Msg.GetState())

	if len(resp.Msg.GetSources()) > 0 {
		fmt.Println("SOURCES:")
		for _, s := range resp.Msg.GetSources() {
			fmt.Printf("  %s\n", s.GetSourceId())
		}
		fmt.Println()
	}

	if len(resp.Msg.GetPipelines()) > 0 {
		fmt.Println("PIPELINES:")
		printPipelineTable(resp.Msg.GetPipelines())
	}
}

func printPipelineTable(pipelines []*v1.PipelineStatus) {
	fmt.Printf("  %-50s  %-12s  %s\n", "PIPELINE", "STATE", "ROWS")
	for _, p := range pipelines {
		fmt.Printf("  %-50s  %-12s  %d\n",
			p.GetPipelineId(),
			p.GetState().String(),
			p.GetRowCount(),
		)
	}
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
	case "prune":
		snapshotPruneCmd(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "unknown snapshot command: %s\n", sub) //nolint:gosec // CLI output
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
	if output == outputJSON {
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

func snapshotPruneCmd(args []string) {
	fs := flag.NewFlagSet("snapshot prune", flag.ExitOnError)
	keep := fs.Int("keep", 0, "number of snapshots to keep (required)")
	parseGlobalFlags(fs, args)

	if *keep <= 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo snapshot prune --keep N")
		os.Exit(1)
	}

	resp, err := oamClient().PruneSnapshots(ctx(), connect.NewRequest(&v1.PruneSnapshotsRequest{
		Keep: int32(*keep), //nolint:gosec // CLI input
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("pruned %d snapshots\n", resp.Msg.GetDeletedCount())
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
		fmt.Fprintf(os.Stderr, "unknown query command: %s\n", sub) //nolint:gosec // CLI output
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

// --- reload ---

func reloadCmd(args []string) {
	fs := flag.NewFlagSet("reload", flag.ExitOnError)
	source := fs.String("source", "", "source ID (required)")
	parseGlobalFlags(fs, args)

	remaining := fs.Args()
	if len(remaining) == 0 || *source == "" {
		fmt.Fprintln(os.Stderr, "usage: laredo reload --source <id> <schema.table>")
		os.Exit(1)
	}

	schema, table, ok := strings.Cut(remaining[0], ".")
	if !ok {
		fmt.Fprintln(os.Stderr, "table must be in schema.table format")
		os.Exit(1)
	}

	resp, err := oamClient().ReloadTable(ctx(), connect.NewRequest(&v1.ReloadTableRequest{
		SourceId: *source,
		Schema:   schema,
		Table:    table,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if resp.Msg.GetAccepted() {
		fmt.Println(resp.Msg.GetMessage())
	} else {
		fmt.Fprintf(os.Stderr, "reload failed: %s\n", resp.Msg.GetMessage())
		os.Exit(1)
	}
}

// --- pause/resume ---

func pauseCmd(args []string) {
	fs := flag.NewFlagSet("pause", flag.ExitOnError)
	source := fs.String("source", "", "source ID (required)")
	parseGlobalFlags(fs, args)

	if *source == "" {
		fmt.Fprintln(os.Stderr, "usage: laredo pause --source <id>")
		os.Exit(1)
	}

	_, err := oamClient().PauseSync(ctx(), connect.NewRequest(&v1.PauseSyncRequest{
		SourceId: *source,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("paused")
}

func resumeCmd(args []string) {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	source := fs.String("source", "", "source ID (required)")
	parseGlobalFlags(fs, args)

	if *source == "" {
		fmt.Fprintln(os.Stderr, "usage: laredo resume --source <id>")
		os.Exit(1)
	}

	_, err := oamClient().ResumeSync(ctx(), connect.NewRequest(&v1.ResumeSyncRequest{
		SourceId: *source,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("resumed")
}

// --- dead-letters ---

func deadLettersCmd(args []string) {
	if len(args) > 0 && args[0] == "purge" {
		deadLettersPurgeCmd(args[1:])
		return
	}

	fs := flag.NewFlagSet("dead-letters", flag.ExitOnError)
	limit := fs.Int("limit", 0, "max entries to return")
	parseGlobalFlags(fs, args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo dead-letters <pipeline-id>")
		os.Exit(1)
	}

	resp, err := oamClient().ListDeadLetters(ctx(), connect.NewRequest(&v1.ListDeadLettersRequest{
		PipelineId: remaining[0],
		Limit:      int32(*limit), //nolint:gosec // CLI input won't overflow
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	entries := resp.Msg.GetEntries()
	if output == outputJSON {
		printJSON(entries)
		return
	}

	if len(entries) == 0 {
		fmt.Println("no dead letters")
		return
	}

	fmt.Printf("%-20s  %-8s  %s\n", "TIMESTAMP", "ACTION", "ERROR")
	for _, e := range entries {
		ts := ""
		if e.GetTimestamp() != nil {
			ts = e.GetTimestamp().AsTime().Format(time.DateTime)
		}
		fmt.Printf("%-20s  %-8s  %s\n", ts, e.GetAction(), e.GetErrorMessage())
	}
	fmt.Printf("\nTotal: %d\n", resp.Msg.GetTotalCount())
}

func deadLettersPurgeCmd(args []string) {
	fs := flag.NewFlagSet("dead-letters purge", flag.ExitOnError)
	parseGlobalFlags(fs, args)

	remaining := fs.Args()
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "usage: laredo dead-letters purge <pipeline-id>")
		os.Exit(1)
	}

	resp, err := oamClient().PurgeDeadLetters(ctx(), connect.NewRequest(&v1.PurgeDeadLettersRequest{
		PipelineId: remaining[0],
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("purged %d dead letters\n", resp.Msg.GetPurged())
}

func printJSON(v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

package laredo_test

import (
	"context"
	"fmt"
	"time"

	"github.com/zourzouvillys/laredo"
	"github.com/zourzouvillys/laredo/source/testsource"
	"github.com/zourzouvillys/laredo/target/memory"
)

func ExampleNewEngine() {
	// Create a test source with sample data.
	src := testsource.New()
	src.SetSchema(laredo.Table("public", "users"), []laredo.ColumnDefinition{
		{Name: "id", Type: "integer", PrimaryKey: true},
		{Name: "name", Type: "text"},
	})
	src.AddRow(laredo.Table("public", "users"), laredo.Row{"id": 1, "name": "alice"})

	// Create an in-memory target.
	target := memory.NewIndexedTarget(memory.LookupFields("name"))

	// Build the engine with a source and pipeline.
	eng, errs := laredo.NewEngine(
		laredo.WithSource("test", src),
		laredo.WithPipeline("test", laredo.Table("public", "users"), target),
	)
	if len(errs) > 0 {
		panic(errs)
	}

	// Start the engine.
	ctx := context.Background()
	if err := eng.Start(ctx); err != nil {
		panic(err)
	}

	// Wait for data to be loaded.
	eng.AwaitReady(5 * time.Second)

	// Query the target.
	row, ok := target.Lookup("alice")
	if ok {
		fmt.Printf("Found: id=%v, name=%v\n", row["id"], row["name"])
	}

	_ = eng.Stop(ctx)
	// Output: Found: id=1, name=alice
}

func ExampleTable() {
	tid := laredo.Table("public", "users")
	fmt.Println(tid.String())
	// Output: public.users
}

func ExampleRow() {
	row := laredo.Row{
		"id":    1,
		"name":  "alice",
		"email": "alice@example.com",
	}
	fmt.Println(row.GetString("name"))
	// Output: alice
}

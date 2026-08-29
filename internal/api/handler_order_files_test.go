package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/orders"
)

// newOrderFilesFixture lays out a city dir with a city-local order and a
// pack-shaped layer, returning both roots.
func newOrderFilesFixture(t *testing.T) (cityDir, packDir string) {
	t.Helper()
	cityDir = t.TempDir()
	packDir = t.TempDir()
	mustWrite := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(cityDir, "orders", "heartbeat.toml"), "[order]\nexec = \"bash "+filepath.Join(cityDir, "assets", "scripts", "heartbeat.sh")+"\"\n")
	mustWrite(filepath.Join(cityDir, "assets", "scripts", "heartbeat.sh"), "#!/bin/bash\necho beat\n")
	mustWrite(filepath.Join(packDir, "orders", "reaper.toml"), "[order]\nexec = \"$PACK_DIR/assets/scripts/reaper.sh\"\n")
	mustWrite(filepath.Join(packDir, "assets", "scripts", "reaper.sh"), "#!/bin/bash\necho reap\n")
	mustWrite(filepath.Join(packDir, "formulas", "nightly.formula.toml"), "description = \"nightly formula\"\n")
	mustWrite(filepath.Join(packDir, "orders", "nightly.toml"), "[order]\nformula = \"nightly\"\n")
	return cityDir, packDir
}

func entryByRole(t *testing.T, files []orderFileEntry, role string) orderFileEntry {
	t.Helper()
	for _, f := range files {
		if f.Role == role {
			return f
		}
	}
	t.Fatalf("no %q entry in %+v", role, files)
	return orderFileEntry{}
}

func TestCollectOrderFilesExecAbsolutePath(t *testing.T) {
	cityDir, _ := newOrderFilesFixture(t)
	script := filepath.Join(cityDir, "assets", "scripts", "heartbeat.sh")
	a := orders.Order{
		Name:   "heartbeat",
		Exec:   "bash " + script,
		Source: filepath.Join(cityDir, "orders", "heartbeat.toml"),
	}

	files := collectOrderFiles(a, cityDir)
	orderEntry := entryByRole(t, files, "order")
	if orderEntry.Language != "toml" || !strings.Contains(orderEntry.Content, "[order]") {
		t.Fatalf("order entry = %+v, want TOML content", orderEntry)
	}
	scriptEntry := entryByRole(t, files, "script")
	if scriptEntry.Error != "" || !strings.Contains(scriptEntry.Content, "echo beat") {
		t.Fatalf("script entry = %+v, want script content", scriptEntry)
	}
	if scriptEntry.Language != "shell" {
		t.Fatalf("script language = %q, want shell", scriptEntry.Language)
	}
}

func TestCollectOrderFilesExpandsPackDir(t *testing.T) {
	cityDir, packDir := newOrderFilesFixture(t)
	a := orders.Order{
		Name:         "reaper",
		Exec:         "$PACK_DIR/assets/scripts/reaper.sh",
		Source:       filepath.Join(packDir, "orders", "reaper.toml"),
		FormulaLayer: filepath.Join(packDir, "formulas"),
	}

	files := collectOrderFiles(a, cityDir)
	scriptEntry := entryByRole(t, files, "script")
	if scriptEntry.Error != "" || !strings.Contains(scriptEntry.Content, "echo reap") {
		t.Fatalf("script entry = %+v, want expanded $PACK_DIR content", scriptEntry)
	}
	if scriptEntry.Ref != "$PACK_DIR/assets/scripts/reaper.sh" {
		t.Fatalf("script ref = %q, want the original unexpanded reference", scriptEntry.Ref)
	}
}

func TestCollectOrderFilesExpandsOrderDirTraversalWithinPack(t *testing.T) {
	_, packDir := newOrderFilesFixture(t)
	// $ORDER_DIR/../assets/... climbs out of orders/ but stays inside the pack.
	a := orders.Order{
		Name:         "reaper",
		Exec:         "bash $ORDER_DIR/../assets/scripts/reaper.sh",
		Source:       filepath.Join(packDir, "orders", "reaper.toml"),
		FormulaLayer: filepath.Join(packDir, "formulas"),
	}
	files := collectOrderFiles(a, t.TempDir())
	scriptEntry := entryByRole(t, files, "script")
	if scriptEntry.Error != "" || !strings.Contains(scriptEntry.Content, "echo reap") {
		t.Fatalf("script entry = %+v, want in-pack traversal to resolve", scriptEntry)
	}
}

func TestCollectOrderFilesRefusesEscapedRoots(t *testing.T) {
	cityDir, _ := newOrderFilesFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.sh")
	if err := os.WriteFile(outside, []byte("#!/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := orders.Order{
		Name:   "heartbeat",
		Exec:   "bash " + outside,
		Source: filepath.Join(cityDir, "orders", "heartbeat.toml"),
	}

	files := collectOrderFiles(a, cityDir)
	scriptEntry := entryByRole(t, files, "script")
	if scriptEntry.Error != "outside serving roots" {
		t.Fatalf("script entry = %+v, want outside-serving-roots error", scriptEntry)
	}
	if scriptEntry.Content != "" {
		t.Fatal("out-of-root entry carried content, want none")
	}
}

func TestCollectOrderFilesMissingScriptSkippedNotErrored(t *testing.T) {
	cityDir, _ := newOrderFilesFixture(t)
	a := orders.Order{
		Name:   "heartbeat",
		Exec:   "bash " + filepath.Join(cityDir, "assets", "scripts", "gone.sh"),
		Source: filepath.Join(cityDir, "orders", "heartbeat.toml"),
	}
	files := collectOrderFiles(a, cityDir)
	// A token that does not stat is command syntax, not a file claim: the
	// manifest carries only the order TOML.
	for _, f := range files {
		if f.Role == "script" {
			t.Fatalf("unexpected script entry %+v for a missing file", f)
		}
	}
	if len(files) != 1 || files[0].Role != "order" {
		t.Fatalf("files = %+v, want just the order TOML", files)
	}
}

func TestCollectOrderFilesResolvesFormula(t *testing.T) {
	cityDir, packDir := newOrderFilesFixture(t)
	a := orders.Order{
		Name:         "nightly",
		Formula:      "nightly",
		Source:       filepath.Join(packDir, "orders", "nightly.toml"),
		FormulaLayer: filepath.Join(packDir, "formulas"),
	}
	files := collectOrderFiles(a, cityDir)
	formulaEntry := entryByRole(t, files, "formula")
	if formulaEntry.Error != "" || !strings.Contains(formulaEntry.Content, "nightly formula") {
		t.Fatalf("formula entry = %+v, want resolved formula TOML", formulaEntry)
	}
}

func TestCollectOrderFilesFormulaMissingFromLayer(t *testing.T) {
	cityDir, packDir := newOrderFilesFixture(t)
	a := orders.Order{
		Name:         "nightly",
		Formula:      "does-not-exist",
		Source:       filepath.Join(packDir, "orders", "nightly.toml"),
		FormulaLayer: filepath.Join(packDir, "formulas"),
	}
	files := collectOrderFiles(a, cityDir)
	formulaEntry := entryByRole(t, files, "formula")
	if formulaEntry.Error == "" || formulaEntry.Content != "" {
		t.Fatalf("formula entry = %+v, want error without content", formulaEntry)
	}
}

func TestCollectOrderFilesTruncatesLargeFile(t *testing.T) {
	cityDir, _ := newOrderFilesFixture(t)
	big := filepath.Join(cityDir, "assets", "scripts", "big.sh")
	if err := os.WriteFile(big, []byte("#"+strings.Repeat("x", orderFileMaxContentBytes+100)), 0o644); err != nil {
		t.Fatal(err)
	}
	a := orders.Order{
		Name:   "heartbeat",
		Exec:   "bash " + big,
		Source: filepath.Join(cityDir, "orders", "heartbeat.toml"),
	}
	files := collectOrderFiles(a, cityDir)
	scriptEntry := entryByRole(t, files, "script")
	if !scriptEntry.Truncated || len(scriptEntry.Content) != orderFileMaxContentBytes {
		t.Fatalf("truncated=%v len=%d, want truncated at %d", scriptEntry.Truncated, len(scriptEntry.Content), orderFileMaxContentBytes)
	}
}

func TestCollectOrderFilesBinaryGuard(t *testing.T) {
	cityDir, _ := newOrderFilesFixture(t)
	bin := filepath.Join(cityDir, "assets", "scripts", "blob.sh")
	if err := os.WriteFile(bin, []byte{'#', 0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	a := orders.Order{
		Name:   "heartbeat",
		Exec:   "bash " + bin,
		Source: filepath.Join(cityDir, "orders", "heartbeat.toml"),
	}
	files := collectOrderFiles(a, cityDir)
	scriptEntry := entryByRole(t, files, "script")
	if scriptEntry.Error != "binary file" || scriptEntry.Content != "" {
		t.Fatalf("script entry = %+v, want binary-file error without content", scriptEntry)
	}
}

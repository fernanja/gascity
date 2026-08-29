package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/api/apierr"
	"github.com/gastownhall/gascity/internal/formula"
	"github.com/gastownhall/gascity/internal/orders"
)

// orderFileMaxContentBytes caps each served file's content. Order TOMLs and
// exec scripts are a few KB; anything larger is truncated, not refused, so
// the viewer still shows the head of an unexpectedly large file.
const orderFileMaxContentBytes = 256 * 1024

// orderFileEntry is one file involved in an order: the order TOML itself,
// the shell scripts its exec/check lines reference, or (for formula orders)
// the formula TOML. Per-entry Error carries unresolvable/unreadable/
// out-of-bounds outcomes so one bad reference never fails the response.
type orderFileEntry struct {
	Role      string `json:"role" enum:"order,script,check,formula" doc:"What this file is to the order."`
	Ref       string `json:"ref" doc:"The reference as written in the order definition (may contain $ORDER_DIR/$PACK_DIR)."`
	Path      string `json:"path" doc:"Resolved absolute path."`
	Language  string `json:"language" doc:"Display language hint (toml, shell, markdown, text)."`
	SizeBytes int64  `json:"size_bytes" doc:"File size in bytes; 0 when the file could not be read."`
	Content   string `json:"content,omitempty" doc:"File content, absent when Error is set."`
	Truncated bool   `json:"truncated,omitempty" doc:"True when Content was clipped at the size cap."`
	Error     string `json:"error,omitempty" doc:"Why this entry has no content (missing, binary, outside serving roots)."`
}

// OrderFilesBody is the response body for GET /v0/city/{cityName}/orders/{name}/files.
type OrderFilesBody struct {
	ScopedName string           `json:"scoped_name" doc:"Scoped order name."`
	Files      []orderFileEntry `json:"files" doc:"The order definition and every file it references, order TOML first."`
}

// OrderFilesInput is the Huma input for GET /v0/city/{cityName}/orders/{name}/files.
type OrderFilesInput struct {
	CityScope
	Name string `path:"name" doc:"Order name or scoped name."`
}

// OrderFilesOutput is the Huma output envelope for GET /v0/city/{cityName}/orders/{name}/files.
type OrderFilesOutput struct {
	Body OrderFilesBody
}

// humaHandleOrderFiles is the Huma-typed handler for GET /v0/city/{cityName}/orders/{name}/files.
func (s *Server) humaHandleOrderFiles(_ context.Context, input *OrderFilesInput) (*OrderFilesOutput, error) {
	a, err := resolveOrder(s.state.OrdersAll(), input.Name)
	if err != nil {
		if errors.Is(err, errOrderAmbiguous) {
			return nil, apierr.AmbiguousReference.Msg(err.Error())
		}
		return nil, apierr.OrderNotFound.Msg(err.Error())
	}

	out := &OrderFilesOutput{}
	out.Body.ScopedName = a.ScopedName()
	out.Body.Files = collectOrderFiles(*a, s.state.CityPath())
	return out, nil
}

// collectOrderFiles builds the file manifest for one order. Serving roots are
// the city directory, the order's own directory, and the order's pack
// directory (dir of the formula layer, which covers pack-cache orders); a
// reference that resolves outside all of them yields an error entry, never
// file bytes.
func collectOrderFiles(a orders.Order, cityPath string) []orderFileEntry {
	roots := orderFileServingRoots(a, cityPath)
	files := make([]orderFileEntry, 0, 4)

	if strings.TrimSpace(a.Source) != "" {
		files = append(files, readOrderFileEntry("order", a.Source, a.Source, roots))
	}

	seen := map[string]bool{filepath.Clean(a.Source): true}
	appendScriptRefs := func(role, commandLine string) {
		for _, ref := range orderCommandFileRefs(a, commandLine) {
			if seen[filepath.Clean(ref.path)] {
				continue
			}
			seen[filepath.Clean(ref.path)] = true
			files = append(files, readOrderFileEntry(role, ref.ref, ref.path, roots))
		}
	}
	appendScriptRefs("script", a.Exec)
	appendScriptRefs("check", a.Check)

	if formulaName := strings.TrimSpace(a.Formula); formulaName != "" {
		if a.FormulaLayer != "" {
			if path, ok := formula.Resolve([]string{a.FormulaLayer}, formulaName); ok {
				if !seen[filepath.Clean(path)] {
					seen[filepath.Clean(path)] = true
					files = append(files, readOrderFileEntry("formula", formulaName, path, roots))
				}
			} else {
				files = append(files, orderFileEntry{
					Role: "formula", Ref: formulaName, Language: "toml",
					Error: "formula not found in the order's formula layer",
				})
			}
		} else {
			files = append(files, orderFileEntry{
				Role: "formula", Ref: formulaName, Language: "toml",
				Error: "order has no formula layer to resolve the formula from",
			})
		}
	}
	return files
}

func orderFileServingRoots(a orders.Order, cityPath string) []string {
	roots := make([]string, 0, 3)
	if strings.TrimSpace(cityPath) != "" {
		roots = append(roots, filepath.Clean(cityPath))
	}
	if strings.TrimSpace(a.Source) != "" {
		roots = append(roots, filepath.Clean(filepath.Dir(a.Source)))
	}
	if strings.TrimSpace(a.FormulaLayer) != "" {
		roots = append(roots, filepath.Clean(filepath.Dir(a.FormulaLayer)))
	}
	return roots
}

type orderFileRef struct {
	ref  string // as written in the order
	path string // expanded absolute path
}

// orderCommandFileRefs extracts the file references from an exec/check
// command line: whitespace tokens that, after $ORDER_DIR/$PACK_DIR expansion
// (the same values order execution injects — see cmd/gc order_store.go),
// are absolute paths to existing regular files. Interpreters ("bash"), flags,
// and unexpanded-variable tokens are skipped, not errored — they are command
// syntax, not file claims.
func orderCommandFileRefs(a orders.Order, commandLine string) []orderFileRef {
	if strings.TrimSpace(commandLine) == "" {
		return nil
	}
	expander := orderDirExpander(a)
	refs := make([]orderFileRef, 0, 2)
	for _, token := range strings.Fields(commandLine) {
		expanded := expander(token)
		if strings.Contains(expanded, "$") || !filepath.IsAbs(expanded) {
			continue
		}
		expanded = filepath.Clean(expanded)
		info, err := os.Stat(expanded)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		refs = append(refs, orderFileRef{ref: token, path: expanded})
	}
	return refs
}

func orderDirExpander(a orders.Order) func(string) string {
	orderDir := ""
	if a.Source != "" {
		orderDir = filepath.Dir(a.Source)
	}
	packDir := ""
	if a.FormulaLayer != "" {
		packDir = filepath.Dir(a.FormulaLayer)
	}
	replacer := strings.NewReplacer(
		"${ORDER_DIR}", orderDir,
		"$ORDER_DIR", orderDir,
		"${PACK_DIR}", packDir,
		"$PACK_DIR", packDir,
	)
	return replacer.Replace
}

func readOrderFileEntry(role, ref, path string, roots []string) orderFileEntry {
	entry := orderFileEntry{
		Role:     role,
		Ref:      ref,
		Path:     filepath.Clean(path),
		Language: orderFileLanguage(path),
	}
	if !pathUnderAnyRoot(entry.Path, roots) {
		entry.Error = "outside serving roots"
		return entry
	}
	info, err := os.Stat(entry.Path)
	if err != nil {
		entry.Error = "file not found"
		return entry
	}
	if !info.Mode().IsRegular() {
		entry.Error = "not a regular file"
		return entry
	}
	entry.SizeBytes = info.Size()

	f, err := os.Open(entry.Path)
	if err != nil {
		entry.Error = "file unreadable"
		return entry
	}
	defer f.Close() //nolint:errcheck // read-only handle close is best-effort

	head, err := io.ReadAll(io.LimitReader(f, orderFileMaxContentBytes+1))
	if err != nil {
		entry.Error = "file unreadable"
		return entry
	}
	if bytes.IndexByte(head, 0) >= 0 {
		entry.Error = "binary file"
		return entry
	}
	if len(head) > orderFileMaxContentBytes {
		head = head[:orderFileMaxContentBytes]
		entry.Truncated = true
	}
	entry.Content = string(head)
	return entry
}

func pathUnderAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if root == "" {
			continue
		}
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func orderFileLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return "toml"
	case ".sh", ".bash":
		return "shell"
	case ".md":
		return "markdown"
	case ".json":
		return "json"
	default:
		return "text"
	}
}

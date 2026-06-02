package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// RepoMap (ADR 036) returns a structural symbol map of a cloned repo:
// "which files define which functions/types/classes." It's the opt-in
// follow-on to repo.fetch for code-understanding tasks — while the
// repo.fetch envelope tells the agent what's in the repo at the file
// level, repo.map answers "where is FunctionX defined" at the symbol
// level, inspired by Aider's repo-map (SWE-bench Lite SOTA 26.3%,
// 2024-05-22).
//
// Input:
//
//	{
//	  "_session_id":   "...",          // session from repo.fetch
//	  "clone_path":    "/tmp/...",     // required
//	  "token_budget":  1000,           // optional, default 1500
//	  "include_globs": ["*.go"]        // optional ctags include filters
//	}
//
// Output:
//
//	{
//	  "map":              "path/to/file.go:\n  function Foo\n  struct Bar\n...",
//	  "tokens_estimated": 947,
//	  "files_covered":    42,
//	  "files_total":      142
//	}
//
// Implementation: runs `ctags -R --output-format=json` in the sidecar,
// groups symbols by file, ranks files by (shallow path, symbol count,
// known-code-dir membership), renders the top-N files inside the token
// budget. Depends on universal-ctags in the sidecar image; fails with a
// clear install hint when ctags is missing.
func RepoMap() *packs.Pack {
	return &packs.Pack{
		Name:         "repo.map",
		Version:      "v1",
		Description:  "Return a symbol-level structural map of a cloned repo (Aider-style), budgeted to a token target.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"clone_path"},
			Properties: map[string]string{
				"clone_path":    "string",
				"token_budget":  "number",
				"include_globs": "array",
				"languages":     "array",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"map", "tokens_estimated"},
			Properties: map[string]string{
				"map":              "string",
				"tokens_estimated": "number",
				"files_covered":    "number",
				"files_total":      "number",
			},
		},
		Handler: repoMapHandler,
	}
}

type repoMapInput struct {
	ClonePath    string   `json:"clone_path"`
	TokenBudget  int      `json:"token_budget"`
	IncludeGlobs []string `json:"include_globs"`
	Languages    []string `json:"languages"`
}

// globToLanguage maps a common file-extension glob (the shape agents
// naturally reach for) to the ctags language name. Anything not in
// this table is ignored silently — `include_globs` stays a best-
// effort hint, not a hard contract. `--languages=` is the real filter
// ctags understands; `--include` is not a real flag.
var globToLanguage = map[string]string{
	"*.go":    "Go",
	"*.py":    "Python",
	"*.js":    "JavaScript",
	"*.jsx":   "JavaScript",
	"*.ts":    "TypeScript",
	"*.tsx":   "TypeScript",
	"*.rs":    "Rust",
	"*.java":  "Java",
	"*.c":     "C",
	"*.cpp":   "C++",
	"*.cc":    "C++",
	"*.h":     "C",
	"*.hpp":   "C++",
	"*.rb":    "Ruby",
	"*.php":   "PHP",
	"*.cs":    "C#",
	"*.kt":    "Kotlin",
	"*.swift": "Swift",
	"*.lua":   "Lua",
	"*.sh":    "Sh",
}

// defaultExcludes are patterns ctags will skip regardless of caller
// input. These are files that pollute the symbol ranking because
// they're parsed into hundreds of "symbols" (every JSON key, every
// lockfile entry, every minified line) while contributing no real
// code-understanding value.
var defaultExcludes = []string{
	"node_modules",
	"vendor",
	".git",
	"dist",
	"build",
	"target",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"go.sum",
	"Cargo.lock",
	"composer.lock",
	"Gemfile.lock",
	"*.min.js",
	"*.min.css",
	"*.map",
}

func repoMapHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in repoMapInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
	}
	if _, perr := safeJoin(in.ClonePath, "", ec); perr != nil {
		return nil, perr
	}
	budget := in.TokenBudget
	if budget <= 0 {
		budget = 1500
	}
	// Whitelist-validate globs before they reach shell. We also accept
	// a `languages` input for callers that know the ctags language
	// name directly — whichever arrives, both go through the same
	// validator.
	for _, g := range in.IncludeGlobs {
		if !isSafeCtagsGlob(g) {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("unsafe include glob: %q", g)}
		}
	}
	for _, lang := range in.Languages {
		if !isSafeCtagsLanguage(lang) {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("unsafe language name: %q", lang)}
		}
	}

	// Translate include_globs → ctags language names. Anything we
	// can't map is dropped silently — the caller still gets a
	// whole-repo scan minus the defaultExcludes.
	mapped := make([]string, 0, len(in.IncludeGlobs)+len(in.Languages))
	for _, g := range in.IncludeGlobs {
		if lang, ok := globToLanguage[g]; ok {
			mapped = append(mapped, lang)
		}
	}
	mapped = append(mapped, in.Languages...)
	script := buildRepoMapScript(in.ClonePath, budget, mapped)
	res, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", script}})
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("repo.map exec: %v", err)}
	}
	if res.ExitCode != 0 {
		stderr := strings.TrimSpace(string(res.Stderr))
		switch {
		case strings.Contains(stderr, "ctags-missing"):
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: "universal-ctags is required for repo.map but is not installed in the sidecar image. Install with `apk add universal-ctags` (Alpine) or `apt-get install universal-ctags` (Debian)."}
		case strings.Contains(stderr, "python3-missing"):
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: "python3 is required for repo.map but is not installed in the sidecar image."}
		default:
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("repo.map exit %d: %s", res.ExitCode, truncateString(stderr, 512))}
		}
	}
	if !json.Valid(res.Stdout) {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("repo.map emitted non-JSON: %q", truncateString(string(res.Stdout), 256))}
	}
	return res.Stdout, nil
}

// isSafeCtagsLanguage validates a ctags `--languages=` value. ctags
// language names are ASCII identifiers (Go, Python, JavaScript, C++,
// C#, F# etc.) — letters, digits, and `+#.-_`. We reject anything
// else to close the shell-injection path.
func isSafeCtagsLanguage(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9':
		case r == '+', r == '#', r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// isSafeCtagsGlob validates that a glob pattern is a bare extension or
// simple wildcard pattern safe to pass to ctags --exclude= without
// shell escaping concerns. ctags patterns are not shell-expanded by
// ctags itself, but the glob will sit inside a sh -c script, so we
// reject anything that could escape its quoting.
func isSafeCtagsGlob(g string) bool {
	if g == "" || len(g) > 64 {
		return false
	}
	for _, r := range g {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9':
		case r == '*', r == '?', r == '[', r == ']',
			r == '.', r == ',', r == '-', r == '_', r == '/':
		default:
			return false
		}
	}
	return true
}

// buildRepoMapScript renders the shell pipeline that drives ctags and
// emits the final {map, tokens_estimated, files_covered, files_total}
// envelope. Structure:
//
//  1. guard on python3 + ctags (emit sentinel stderr on missing, exit non-zero)
//  2. write the Python reducer to a temp file (avoids heredoc/stdin conflict)
//  3. pipe ctags stdout through the reducer, reducer prints final JSON
func buildRepoMapScript(clonePath string, tokenBudget int, languages []string) string {
	// Language restriction: pass as `--languages=Go,Python,...` — the
	// one flag ctags actually recognizes for narrowing file scope.
	// Empty list means "all languages."
	var langs strings.Builder
	if len(languages) > 0 {
		langs.WriteString(" --languages=")
		langs.WriteString(shellQuote(strings.Join(languages, ",")))
	}
	// Default excludes for lockfiles, minified bundles, and vendor
	// trees. These pollute the symbol ranking because ctags extracts
	// every JSON key / lockfile entry as a "symbol" and drowns real
	// code under a file no human would read.
	var excludes strings.Builder
	for _, pat := range defaultExcludes {
		excludes.WriteString(" --exclude=")
		excludes.WriteString(shellQuote(pat))
	}
	return fmt.Sprintf(`set -eu
CLONE_PATH=%s
if ! command -v python3 >/dev/null 2>&1; then
  echo "python3-missing" >&2
  exit 2
fi
if ! command -v ctags >/dev/null 2>&1; then
  echo "ctags-missing" >&2
  exit 3
fi
SCRIPT=$(mktemp /tmp/helmdeck-repomap-XXXXXX.py)
trap 'rm -f "$SCRIPT"' EXIT
cat > "$SCRIPT" <<'PYEOF'
%s
PYEOF
ctags -R --output-format=json --fields=+K+n%s%s "$CLONE_PATH" 2>/dev/null \
  | CLONE_PATH="$CLONE_PATH" TOKEN_BUDGET=%d python3 "$SCRIPT"
`, shellQuote(clonePath), repoMapPythonReducer, excludes.String(), langs.String(), tokenBudget)
}

// repoMapPythonReducer is the Python program that reads ctags JSON on
// stdin and emits the final {map, tokens_estimated, ...} JSON on
// stdout. Kept as a separate raw-string so tests can invoke the
// reducer directly without going through shell.
const repoMapPythonReducer = `import json, os, sys, collections, subprocess

clone_path = os.environ["CLONE_PATH"]
budget     = int(os.environ["TOKEN_BUDGET"])
raw        = sys.stdin.read()

tags_by_file = collections.defaultdict(list)
for line in raw.splitlines():
    line = line.strip()
    if not line:
        continue
    try:
        t = json.loads(line)
    except ValueError:
        continue
    path = t.get("path", "")
    if path.startswith(clone_path):
        path = os.path.relpath(path, clone_path)
    kind = t.get("kind", "")
    name = t.get("name", "")
    lineno = t.get("line", 0)
    if not (path and name):
        continue
    tags_by_file[path].append((lineno, kind, name))

try:
    git = subprocess.run(
        ["git", "-C", clone_path, "ls-files"],
        capture_output=True, text=True, check=True,
    ).stdout
    files_total = sum(1 for l in git.splitlines() if l.strip())
except Exception:
    files_total = len(tags_by_file)

CODE_DIRS = ("src", "cmd", "lib", "internal", "pkg", "app")

def rank(path):
    depth     = path.count("/")
    sym_count = len(tags_by_file[path])
    in_code   = any(path.startswith(d + "/") for d in CODE_DIRS)
    return (-sym_count, depth, 0 if in_code else 1, path)

ranked = sorted(tags_by_file.keys(), key=rank)

CHARS_PER_TOKEN = 4
char_budget = budget * CHARS_PER_TOKEN

out_lines = []
chars_used = 0
files_covered = 0

for path in ranked:
    syms = sorted(tags_by_file[path])
    block = [path + ":"]
    for (_lineno, kind, name) in syms:
        block.append("  " + (kind + " " if kind else "") + name)
    block_text = "\n".join(block) + "\n"
    if chars_used + len(block_text) > char_budget and out_lines:
        break
    out_lines.append(block_text)
    chars_used += len(block_text)
    files_covered += 1

map_text = "".join(out_lines)
tokens_estimated = (len(map_text) + CHARS_PER_TOKEN - 1) // CHARS_PER_TOKEN

print(json.dumps({
    "map":              map_text,
    "tokens_estimated": tokens_estimated,
    "files_covered":    files_covered,
    "files_total":      files_total,
}))
`

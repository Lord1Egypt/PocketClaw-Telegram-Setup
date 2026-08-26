// Package deployconfig holds invariants about how this repository is built and
// deployed. It has no runtime code — only tests, which run as part of
// `go test ./...` and therefore in CI on every push.
//
// These exist because a deployment configuration mistake is invisible locally.
// Everything compiles, every test passes, and the failure appears only in a
// Vercel build log after the operator has already been asked for secrets.
package deployconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot resolves the repository root from this package's location.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %q has no go.mod: %v", root, err)
	}
	return root
}

func readVercelJSON(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "vercel.json"))
	if err != nil {
		t.Fatalf("read vercel.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("vercel.json is not valid JSON: %v", err)
	}
	return parsed
}

// Vercel supports two mutually exclusive Go modes. The framework preset builds
// a server from a detected entrypoint; the functions mode turns each api/*.go
// into a serverless function. A repository that satisfies both gets the
// framework preset, api/ is never scanned, and any `functions` entry pointing
// into api/ matches nothing:
//
//	Error: The pattern "api/index.go" defined in `functions` doesn't match any
//	Serverless Functions inside the `api` directory.
//
// That is a build failure the operator meets *after* entering their secrets.
func TestExactlyOneVercelBuildModeIsSatisfied(t *testing.T) {
	root := repoRoot(t)

	// Mode A: the framework preset. Requires a root go.mod and one of these.
	presetEntrypoints := []string{"main.go", "cmd/api/main.go", "cmd/server/main.go"}
	var foundEntrypoints []string
	for _, entrypoint := range presetEntrypoints {
		if _, err := os.Stat(filepath.Join(root, entrypoint)); err == nil {
			foundEntrypoints = append(foundEntrypoints, entrypoint)
		}
	}

	// Mode B: serverless functions. Any .go file under api/ creates one.
	var apiFunctions []string
	apiDir := filepath.Join(root, "api")
	if entries, err := os.ReadDir(apiDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
				apiFunctions = append(apiFunctions, filepath.Join("api", entry.Name()))
			}
		}
	}

	switch {
	case len(foundEntrypoints) > 0 && len(apiFunctions) > 0:
		t.Fatalf("this repository satisfies BOTH Vercel Go build modes, which fails the build.\n"+
			"  framework preset entrypoints: %v\n"+
			"  serverless functions:         %v\n"+
			"Pick one. This project uses the framework preset; delete the api/ functions.",
			foundEntrypoints, apiFunctions)
	case len(foundEntrypoints) == 0 && len(apiFunctions) == 0:
		t.Fatal("this repository satisfies NEITHER Vercel Go build mode; there is nothing to deploy")
	}
}

func TestVercelJSONDeclaresTheGoFrameworkPreset(t *testing.T) {
	// Running a Go server on Vercel requires the framework preset to be set
	// explicitly. Relying on auto-detection alone is what made the previous
	// misconfiguration silent.
	config := readVercelJSON(t)
	framework, ok := config["framework"]
	if !ok {
		t.Fatal(`vercel.json does not set "framework"; a Go server requires "framework": "go"`)
	}
	if framework != "go" {
		t.Fatalf(`vercel.json sets "framework": %q, want "go"`, framework)
	}
}

func TestVercelJSONHasNoDanglingFunctionOrRoutePaths(t *testing.T) {
	root := repoRoot(t)
	config := readVercelJSON(t)

	// Every path named in `functions` must exist, or the build fails with the
	// unmatched-pattern error.
	if functions, ok := config["functions"].(map[string]any); ok {
		for pattern := range functions {
			if _, err := os.Stat(filepath.Join(root, pattern)); err != nil {
				t.Fatalf("vercel.json `functions` names %q, which does not exist in the repository", pattern)
			}
		}
	}

	// Rewrites into /api are meaningless under the framework preset, where the
	// server does its own routing.
	if rewrites, ok := config["rewrites"].([]any); ok {
		for _, entry := range rewrites {
			rewrite, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			destination, _ := rewrite["destination"].(string)
			if strings.HasPrefix(destination, "/api/") {
				t.Fatalf("vercel.json rewrites to %q, but there are no api/ functions under the framework preset",
					destination)
			}
		}
	}
}

// The framework preset runs the built binary and expects it to listen on the
// port it is given. A hardcoded port produces a deployment that builds cleanly
// and then never answers a request.
func TestServerEntrypointListensOnPORT(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(repoRoot(t), "cmd", "server", "main.go"))
	if err != nil {
		t.Fatalf("read cmd/server/main.go: %v", err)
	}
	if !strings.Contains(string(source), `os.Getenv("PORT")`) {
		t.Fatal(`cmd/server/main.go does not read os.Getenv("PORT"); Vercel's Go preset requires it`)
	}
}

// The Deploy button must only ask for values the operator can supply before
// the project exists. Storage credentials are injected by an integration that
// attaches to an already-created project, so requiring them up front makes the
// one-click flow impossible to complete.
func TestDeployButtonDoesNotRequireStorageCredentials(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(raw)

	const marker = "vercel.com/new/clone?"
	if !strings.Contains(readme, marker) {
		t.Fatal("README.md has no Deploy to Vercel link")
	}

	storageVariables := []string{
		"KV_REST_API_URL", "KV_REST_API_TOKEN",
		"UPSTASH_REDIS_REST_URL", "UPSTASH_REDIS_REST_TOKEN",
		"REDIS_REST_URL", "REDIS_REST_TOKEN",
	}
	for _, line := range strings.Split(readme, "\n") {
		index := strings.Index(line, marker)
		if index < 0 {
			continue
		}
		link := line[index:]
		if end := strings.IndexAny(link, ") \t"); end >= 0 {
			link = link[:end]
		}
		envIndex := strings.Index(link, "env=")
		if envIndex < 0 {
			continue
		}
		envList := link[envIndex+len("env="):]
		if amp := strings.Index(envList, "&"); amp >= 0 {
			envList = envList[:amp]
		}
		for _, variable := range storageVariables {
			if strings.Contains(envList, variable) {
				t.Fatalf("the Deploy to Vercel link requires %s, which cannot exist before the "+
					"project does; storage is connected afterwards from the Storage tab", variable)
			}
		}
	}
}

// A committed .env would put live credentials in a public repository.
func TestNoEnvFileIsCommitted(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".env")); err == nil {
		t.Fatal(".env exists in the repository; it must never be committed")
	}
	ignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(ignore), ".env") {
		t.Fatal(".gitignore does not exclude .env")
	}
}

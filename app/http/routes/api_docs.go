package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/swaggo/http-swagger/v2"
)

type routeEntry struct {
	Path    string
	Methods []string
}

type routeCatalog struct {
	mu     sync.RWMutex
	routes []routeEntry
}

func newRouteCatalog() *routeCatalog {
	return &routeCatalog{routes: make([]routeEntry, 0)}
}

func (c *routeCatalog) add(path string, methods ...string) {
	if c == nil {
		return
	}

	normalizedMethods := normalizeMethods(methods)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.routes = append(c.routes, routeEntry{
		Path:    path,
		Methods: normalizedMethods,
	})
}

func (c *routeCatalog) snapshot() []routeEntry {
	if c == nil {
		return nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]routeEntry, len(c.routes))
	copy(result, c.routes)
	return result
}

func normalizeMethods(methods []string) []string {
	if len(methods) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	result := make([]string, 0, len(methods))
	for _, method := range methods {
		normalized := strings.ToUpper(strings.TrimSpace(method))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}

	if len(result) == 0 {
		return nil
	}

	return result
}

func registerDocsRoutes(mux *http.ServeMux, catalog *routeCatalog) {
	mux.HandleFunc("/api-docs/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		spec := buildOpenAPISpec(catalog)
		payload, err := json.Marshal(spec)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to generate openapi: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})

	mux.Handle("/api-docs/", httpSwagger.Handler(
		httpSwagger.URL("/api-docs/openapi.json"),
		httpSwagger.DocExpansion("none"),
		httpSwagger.DeepLinking(true),
	))
}

func buildOpenAPISpec(catalog *routeCatalog) *openapi3.T {
	paths := openapi3.NewPaths()
	entries := catalog.snapshot()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})

	for _, entry := range entries {
		pathItem := paths.Value(entry.Path)
		if pathItem == nil {
			pathItem = &openapi3.PathItem{}
		}

		for _, method := range entry.Methods {
			operation := &openapi3.Operation{
				Summary:     fmt.Sprintf("%s %s", method, entry.Path),
				OperationID: operationID(method, entry.Path),
				RequestBody: buildRequestBodyForMethod(method),
				Responses: openapi3.NewResponses(
					openapi3.WithStatus(http.StatusOK, &openapi3.ResponseRef{Value: &openapi3.Response{Description: ptr("OK")}}),
					openapi3.WithStatus(http.StatusBadRequest, &openapi3.ResponseRef{Value: &openapi3.Response{Description: ptr("Bad Request")}}),
					openapi3.WithStatus(http.StatusUnauthorized, &openapi3.ResponseRef{Value: &openapi3.Response{Description: ptr("Unauthorized")}}),
					openapi3.WithStatus(http.StatusNotFound, &openapi3.ResponseRef{Value: &openapi3.Response{Description: ptr("Not Found")}}),
					openapi3.WithStatus(http.StatusInternalServerError, &openapi3.ResponseRef{Value: &openapi3.Response{Description: ptr("Internal Server Error")}}),
				),
			}

			switch method {
			case http.MethodGet:
				pathItem.Get = operation
			case http.MethodPost:
				pathItem.Post = operation
			case http.MethodPut:
				pathItem.Put = operation
			case http.MethodPatch:
				pathItem.Patch = operation
			case http.MethodDelete:
				pathItem.Delete = operation
			case http.MethodOptions:
				pathItem.Options = operation
			case http.MethodHead:
				pathItem.Head = operation
			}
		}

		paths.Set(entry.Path, pathItem)
	}

	return &openapi3.T{
		OpenAPI: "3.0.3",
		Info: &openapi3.Info{
			Title:       "Nursorgate HTTP API",
			Version:     "auto-generated",
			Description: "Auto-generated from app/http/routes/routes.go registrations.",
		},
		Paths: paths,
	}
}

func operationID(method string, path string) string {
	replacer := strings.NewReplacer("/", "_", "-", "_", "{", "", "}", "", ".", "_")
	cleanPath := strings.Trim(replacer.Replace(path), "_")
	if cleanPath == "" {
		cleanPath = "root"
	}
	return strings.ToLower(method) + "_" + cleanPath
}

func ptr(value string) *string {
	return &value
}

func buildRequestBodyForMethod(method string) *openapi3.RequestBodyRef {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return &openapi3.RequestBodyRef{
			Value: &openapi3.RequestBody{
				Required: true,
				Content: openapi3.Content{
					"application/json": &openapi3.MediaType{
						Schema: &openapi3.SchemaRef{
							Value: &openapi3.Schema{
								Type: &openapi3.Types{"object"},
							},
						},
						Example: map[string]interface{}{},
					},
				},
			},
		}
	default:
		return nil
	}
}
